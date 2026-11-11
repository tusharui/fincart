// Package models defines the domain models for the bank API.
package models

import (
	"time"

	"github.com/google/uuid"
)

// Account represents a tokenized customer card and its balance.
// Raw PAN and CVV are never persisted; only the provider token and last4 are stored.
type Account struct {
	ID                    uuid.UUID `db:"id" gorm:"column:id;primaryKey"`
	MerchantID            uuid.UUID `db:"merchant_id" gorm:"column:merchant_id;not null"`
	CardToken             string    `db:"card_token" gorm:"column:card_token"`
	Last4                 string    `db:"last4" gorm:"column:last4"`
	ExpiryMonth           int       `db:"expiry_month" gorm:"column:expiry_month"`
	ExpiryYear            int       `db:"expiry_year" gorm:"column:expiry_year"`
	BalanceCents          int64     `db:"balance_cents" gorm:"column:balance_cents"`
	AvailableBalanceCents int64     `db:"available_balance_cents" gorm:"column:available_balance_cents"`
	CreatedAt             time.Time `db:"created_at" gorm:"column:created_at"`
	UpdatedAt             time.Time `db:"updated_at" gorm:"column:updated_at"`
}
