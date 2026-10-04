package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/id"
	"github.com/luckydiss/studlance_ai/internal/logging"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/token"
)

func createUser(dataDir, email, role, name, password string) error {
	if password == "" {
		return errors.New("empty password")
	}
	ctx := context.Background()
	st, err := openStore(ctx, dataDir)
	if err != nil {
		return err
	}
	if logFile := logging.SetupStderr(dataDir); logFile != nil {
		defer func() { _ = logFile.Close() }()
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	u := store.User{
		ID:           id.New(),
		Email:        email,
		PasswordHash: hash,
		Role:         store.Role(role),
		Name:         name,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("user with email %s already exists", email)
		}
		return err
	}
	fmt.Printf("created user %s (%s, role %s)\n", u.ID, u.Email, u.Role)
	return nil
}

func createWorker(dataDir, name string) error {
	ctx := context.Background()
	st, err := openStore(ctx, dataDir)
	if err != nil {
		return err
	}
	if logFile := logging.SetupStderr(dataDir); logFile != nil {
		defer func() { _ = logFile.Close() }()
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	secret := token.New()
	w := store.Worker{
		ID:           id.New(),
		Name:         name,
		TokenHash:    token.Hash(secret),
		Capabilities: "[]",
		Info:         "{}",
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateWorker(ctx, w); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("worker %q already exists", name)
		}
		return err
	}
	fmt.Printf("worker_id: %s\n", w.ID)
	fmt.Printf("token: %s\n", secret)
	return nil
}
