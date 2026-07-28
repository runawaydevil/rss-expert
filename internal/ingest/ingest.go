package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/runawaydevil/rss-expert/internal/convergence"
	"github.com/runawaydevil/rss-expert/internal/feed"
)

const (
	CleanRetention    = 30 * 24 * time.Hour
	compressionLevel  = zstd.SpeedDefault
	observationsBatch = 500
	compressionWindow = 1 << 20
	decompressCeiling = 64 << 20
)

var (
	encoder, _ = zstd.NewWriter(nil,
		zstd.WithEncoderLevel(compressionLevel),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(compressionWindow))
	decoder, _ = zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(decompressCeiling))
)

type Result struct {
	PayloadStored bool
	Observations  int
	Converged     int
}

func (s *Store) Ingest(ctx context.Context, source *Source, body []byte, mediaType string, parsed *feed.Feed) (Result, error) {
	var result Result
	now := time.Now().UTC()

	sum := sha256.Sum256(body)
	stored, err := s.storePayload(ctx, sum[:], body, mediaType, now)
	if err != nil {
		return result, err
	}
	result.PayloadStored = stored

	if parsed.Title != "" || parsed.Link != "" || parsed.Self != "" {
		if err := s.DescribeFeed(ctx, source.ID, parsed.Title, parsed.Link, parsed.Self); err != nil {
			return result, err
		}
	}

	touched := make(map[string]struct{}, len(parsed.Items))
	for i := range parsed.Items {
		item := &parsed.Items[i]
		key := itemKey(item)
		if key == "" {
			continue
		}

		inserted, err := s.recordObservation(ctx, source, sum[:], key, item, now)
		if err != nil {
			return result, err
		}
		if inserted {
			result.Observations++
		}
		touched[key] = struct{}{}
	}

	for key := range touched {
		changed, err := s.Converge(ctx, key, now)
		if err != nil {
			return result, err
		}
		if changed {
			result.Converged++
		}
	}
	return result, nil
}

func (s *Store) storePayload(ctx context.Context, sum, body []byte, mediaType string, now time.Time) (bool, error) {
	compressed := encoder.EncodeAll(body, nil)

	res, err := s.db.Write.ExecContext(ctx,
		`insert into raw_payload (sha256, body, byte_length, media_type, first_seen, keep_until)
		 values (?, ?, ?, ?, ?, ?) on conflict (sha256) do nothing`,
		sum, compressed, len(body), nullable(mediaType), now.Unix(), now.Add(CleanRetention).Unix())
	if err != nil {
		return false, fmt.Errorf("ingest: store payload: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Payload(ctx context.Context, sum []byte) ([]byte, error) {
	var compressed []byte
	err := s.db.Read.QueryRowContext(ctx, `select body from raw_payload where sha256 = ?`, sum).Scan(&compressed)
	if err != nil {
		return nil, err
	}
	return decoder.DecodeAll(compressed, nil)
}

func (s *Store) recordObservation(ctx context.Context, source *Source, payload []byte, key string, item *feed.Item, now time.Time) (bool, error) {
	content := contentHash(item)

	res, err := s.db.Write.ExecContext(ctx,
		`insert into observation (
			source_id, payload_sha256, item_key, observed_at, published_at, updated_at,
			title, html, markdown, author, link, guid_is_permalink, in_reply_to,
			comments_url, comments_count, origin_name, origin_url, passthrough,
			fidelity, claimed_by_author, content_hash, enclosures)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (source_id, item_key, content_hash) do nothing`,
		source.ID, payload, key, now.Unix(), unixOrNil(item.Published), unixOrNil(effectiveUpdated(item)),
		nullable(item.Title), nullable(item.HTML), nullable(item.Markdown), nullable(item.Author),
		nullable(item.Link), boolToInt(item.GUIDIsPermalink), nullable(item.InReplyTo),
		commentsURL(item), commentsCount(item), originName(item), originURL(item),
		encodePassthrough(item.Unknown),
		fidelity(item), boolToInt(claimedByAuthor(source, item, key)), content,
		encodeEnclosures(item.Enclosures))
	if err != nil {
		return false, fmt.Errorf("ingest: record observation: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Converge(ctx context.Context, key string, now time.Time) (bool, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, claimed_by_author, coalesce(updated_at, published_at, observed_at),
		        fidelity, content_hash, coalesce(enclosures, '')
		 from observation where item_key = ? order by id`, key)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	var candidates []convergence.Candidate
	for rows.Next() {
		var (
			c          convergence.Candidate
			claimed    int
			updated    int64
			enclosures string
		)
		if err := rows.Scan(&c.ID, &claimed, &updated, &c.Fidelity, &c.ContentHash, &enclosures); err != nil {
			return false, err
		}
		if enclosures != "" {
			var attachments []feed.Enclosure
			if err := json.Unmarshal([]byte(enclosures), &attachments); err != nil {
				return false, fmt.Errorf("ingest: decode observation enclosures: %w", err)
			}
			c.Attachments = len(attachments)
		}
		c.ClaimedByAuthor = claimed == 1
		c.Updated = time.Unix(updated, 0).UTC()
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		return false, nil
	}

	resolved, err := convergence.Resolve(candidates)
	if err != nil {
		return false, err
	}

	var published, updated any
	var inReplyTo any
	err = s.db.Read.QueryRowContext(ctx,
		`select published_at, updated_at, in_reply_to from observation where id = ?`, resolved.Winner.ID).
		Scan(&published, &updated, &inReplyTo)
	if err != nil {
		return false, err
	}

	res, err := s.db.Write.ExecContext(ctx,
		`insert into logical_item (item_key, winner_id, reason, published_at, updated_at, in_reply_to, converged_at)
		 values (?, ?, ?, ?, ?, ?, ?)
		 on conflict (item_key) do update set
		   winner_id = excluded.winner_id, reason = excluded.reason,
		   published_at = excluded.published_at, updated_at = excluded.updated_at,
		   in_reply_to = excluded.in_reply_to, converged_at = excluded.converged_at
		 where logical_item.winner_id <> excluded.winner_id`,
		key, resolved.Winner.ID, string(resolved.Reason), published, updated, inReplyTo, now.Unix())
	if err != nil {
		return false, fmt.Errorf("ingest: converge %s: %w", key, err)
	}
	changed, _ := res.RowsAffected()
	if err := s.index(ctx, key); err != nil {
		return changed > 0, fmt.Errorf("ingest: index %s: %w", key, err)
	}
	return changed > 0, nil
}

func itemKey(item *feed.Item) string {
	if item.GUID != "" {
		return item.GUID
	}
	return item.Link
}

func effectiveUpdated(item *feed.Item) time.Time {
	if !item.Updated.IsZero() {
		return item.Updated
	}
	return item.Published
}

func fidelity(item *feed.Item) int {
	switch {
	case item.Markdown != "":
		return convergence.FidelityMarkdown
	case item.HTML != "":
		return convergence.FidelityHTML
	}
	return convergence.FidelityNothing
}

func claimedByAuthor(source *Source, item *feed.Item, key string) bool {
	sourceHost := source.Host()
	if sourceHost == "" {
		return false
	}
	for _, candidate := range []string{key, item.Link} {
		u, err := url.Parse(candidate)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if sameSite(sourceHost, strings.ToLower(u.Hostname())) {
			return true
		}
	}
	return false
}

func sameSite(a, b string) bool {
	if a == b {
		return true
	}
	return strings.TrimPrefix(a, "www.") == strings.TrimPrefix(b, "www.")
}

func contentHash(item *feed.Item) []byte {
	h := sha256.New()
	for _, part := range []string{
		item.Title, item.HTML, item.Markdown, item.Link,
		item.InReplyTo, item.Author,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	for _, enclosure := range item.Enclosures {
		h.Write([]byte(enclosure.URL))
		h.Write([]byte{0})
		h.Write([]byte(enclosure.Type))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(enclosure.Length, 10)))
		h.Write([]byte{0})
	}
	h.Write([]byte(effectiveUpdated(item).UTC().Format(time.RFC3339)))
	return h.Sum(nil)
}

func encodeEnclosures(list []feed.Enclosure) any {
	if len(list) == 0 {
		return nil
	}
	out, err := json.Marshal(list)
	if err != nil {
		return nil
	}
	return string(out)
}

func encodePassthrough(elements []feed.Element) any {
	if len(elements) == 0 {
		return nil
	}
	out, err := xml.Marshal(struct {
		XMLName  xml.Name       `xml:"passthrough"`
		Elements []feed.Element `xml:"element"`
	}{Elements: elements})
	if err != nil {
		return nil
	}
	return string(out)
}

func commentsURL(item *feed.Item) any {
	if item.Comments != nil && item.Comments.FeedURL != "" {
		return item.Comments.FeedURL
	}
	return nil
}

func commentsCount(item *feed.Item) any {
	if item.Comments != nil {
		return item.Comments.Count
	}
	return nil
}

func originName(item *feed.Item) any {
	if item.Source != nil && item.Source.Name != "" {
		return item.Source.Name
	}
	return nil
}

func originURL(item *feed.Item) any {
	if item.Source != nil && item.Source.URL != "" {
		return item.Source.URL
	}
	return nil
}

func unixOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Unix()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) EnsureLocalSource(ctx context.Context, feedURL, title string) (*Source, error) {
	source, err := s.AddSource(ctx, feedURL)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Write.ExecContext(ctx,
		`update source set is_local = 1, title = ?, next_poll_at = ? where id = ?`,
		nullable(title), int64(1)<<62, source.ID)
	if err != nil {
		return nil, err
	}
	return s.SourceByID(ctx, source.ID)
}

func (s *Store) EnsureFederatedSource(ctx context.Context, actorURL, title string) (*Source, error) {
	source, err := s.AddSource(ctx, actorURL)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Write.ExecContext(ctx,
		`update source set is_local = 0, protocol = ?, title = ?, site_url = ?
		 where id = ?`,
		ProtocolActivityPub, nullable(title), nullable(actorURL), source.ID)
	if err != nil {
		return nil, err
	}
	return s.SourceByID(ctx, source.ID)
}

func (s *Store) IngestItem(ctx context.Context, source *Source, payload []byte, item *feed.Item) error {
	now := time.Now().UTC()

	sum := sha256.Sum256(payload)
	if _, err := s.storePayload(ctx, sum[:], payload, "text/markdown", now); err != nil {
		return err
	}

	key := itemKey(item)
	if key == "" {
		return fmt.Errorf("ingest: item has no key")
	}
	if _, err := s.recordObservation(ctx, source, sum[:], key, item, now); err != nil {
		return err
	}
	_, err := s.Converge(ctx, key, now)
	return err
}

func (s *Store) index(ctx context.Context, key string) error {
	var title, html, markdown, author string
	err := s.db.Read.QueryRowContext(ctx,
		`select coalesce(o.title, ''), coalesce(o.html, ''), coalesce(o.markdown, ''),
		        coalesce(o.author, coalesce(o.origin_name, ''))
		 from logical_item l join observation o on o.id = l.winner_id
		 where l.item_key = ?`, key).Scan(&title, &html, &markdown, &author)
	if err != nil {
		return err
	}

	body := markdown
	if body == "" {
		body = stripTags(html)
	}

	if _, err := s.db.Write.ExecContext(ctx,
		`delete from item_search where item_key = ?`, key); err != nil {
		return err
	}
	_, err = s.db.Write.ExecContext(ctx,
		`insert into item_search (item_key, title, body, author) values (?, ?, ?, ?)`,
		key, title, body, author)
	return err
}

func stripTags(html string) string {
	var b strings.Builder
	depth := 0
	for _, r := range html {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
			b.WriteByte(' ')
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func (s *Store) PurgeExpiredPayloads(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.Write.ExecContext(ctx,
		`delete from raw_payload
		 where keep_until is not null and keep_until < ?
		   and sha256 not in (select payload_sha256 from observation)`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("ingest: purge payloads: %w", err)
	}
	return res.RowsAffected()
}
