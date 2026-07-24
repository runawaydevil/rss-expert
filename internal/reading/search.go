package reading

import (
	"context"
	"strings"
	"unicode"
)

type Hit struct {
	Key     string
	Title   string
	Snippet string
	Author  string
	Rank    float64
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	expression := ToMatchExpression(query)
	if expression == "" {
		return nil, nil
	}

	rows, err := s.db.Read.QueryContext(ctx,
		`select item_key, title,
		        snippet(item_search, 2, '', '', '…', 24),
		        author, rank
		 from item_search where item_search match ?
		 order by rank limit ?`, expression, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Key, &h.Title, &h.Snippet, &h.Author, &h.Rank); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func ToMatchExpression(query string) string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '"' && r != '*'
	})

	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, `"`)
		if field == "" {
			continue
		}
		trailing := ""
		if strings.HasSuffix(field, "*") {
			field = strings.TrimSuffix(field, "*")
			trailing = "*"
		}
		if field == "" {
			continue
		}
		terms = append(terms, `"`+field+`"`+trailing)
	}
	return strings.Join(terms, " AND ")
}
