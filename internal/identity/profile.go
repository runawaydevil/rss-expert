package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	MaxDisplayName = 80
	MaxBio         = 500
)

type Profile struct {
	DisplayName string
	Bio         string
	AvatarSHA   string
	BannerSHA   string
}

func (p Profile) Empty() bool {
	return p.DisplayName == "" && p.Bio == "" && p.AvatarSHA == "" && p.BannerSHA == ""
}

func (s *Store) Profile(ctx context.Context, accountID int64) (Profile, error) {
	var p Profile
	err := s.db.Read.QueryRowContext(ctx,
		`select display_name, bio, avatar_sha, banner_sha from account where id = ?`, accountID).
		Scan(&p.DisplayName, &p.Bio, &p.AvatarSHA, &p.BannerSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNoAccount
	}
	if err != nil {
		return Profile{}, fmt.Errorf("identity: read profile: %w", err)
	}
	return p, nil
}

func (s *Store) SetProfile(ctx context.Context, accountID int64, p Profile) error {
	p.DisplayName = clamp(p.DisplayName, MaxDisplayName)
	p.Bio = clamp(p.Bio, MaxBio)

	res, err := s.db.Write.ExecContext(ctx,
		`update account set display_name = ?, bio = ?, avatar_sha = ?, banner_sha = ? where id = ?`,
		p.DisplayName, p.Bio, p.AvatarSHA, p.BannerSHA, accountID)
	if err != nil {
		return fmt.Errorf("identity: set profile: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoAccount
	}
	return nil
}

func clamp(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
