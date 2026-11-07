// Package models defines the domain models for the bank API.
package models

import (
	"time"

	"github.com/google/uuid"
)

// Account represents a tokenized customer card and its balance.
// Raw PAN and CVV are never persisted; only the provider token and last4 are stored.
type Account struct {
	CreatedAt             time.Time `db:"created_at"`
	UpdatedAt             time.Time `db:"updated_at"`
	CardToken             string    `db:"card_token"`
	Last4                 string    `db:"last4"`
	BalanceCents          int64     `db:"balance_cents"`
	AvailableBalanceCents int64     `db:"available_balance_cents"`
	ExpiryMonth           int       `db:"expiry_month"`
	ExpiryYear            int       `db:"expiry_year"`
	ID                    uuid.UUID `db:"id"`
}
