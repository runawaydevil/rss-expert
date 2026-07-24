package identity

import (
	"context"
	"errors"
	"log/slog"
)

type BootstrapResult int

const (
	BootstrapSkipped BootstrapResult = iota
	BootstrapCreated
	BootstrapOwnerAlreadyExists
)

func Bootstrap(ctx context.Context, s *Store, email, password string, log *slog.Logger) (BootstrapResult, error) {
	if email == "" && password == "" {
		return BootstrapSkipped, nil
	}
	if email == "" || password == "" {
		return BootstrapSkipped, errors.New(
			"identity: RSS_EXPERT_ADMIN_EMAIL and RSS_EXPERT_ADMIN_PASSWORD must be set together")
	}

	owner, err := s.Owner(ctx)
	if err != nil && !errors.Is(err, ErrNoAccount) {
		return BootstrapSkipped, err
	}

	if owner != nil {
		log.Warn("owner already exists; the admin credentials in the environment were ignored. "+
			"Unset RSS_EXPERT_ADMIN_EMAIL and RSS_EXPERT_ADMIN_PASSWORD: they are no longer read, "+
			"and leaving a password in the environment is a standing risk",
			"owner", owner.Email)
		return BootstrapOwnerAlreadyExists, nil
	}

	account, err := s.Create(ctx, email, password, RoleOwner)
	if err != nil {
		return BootstrapSkipped, err
	}

	log.Info("created the owner account from the environment", "email", account.Email)
	log.Warn("the owner password is in this process's environment. Sign in, change it if you like, " +
		"then unset RSS_EXPERT_ADMIN_PASSWORD and restart")

	return BootstrapCreated, nil
}
