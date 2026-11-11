package models

import (
	"time"

	"github.com/google/uuid"
)

// DefaultMerchantID identifies the merchant that owns data created before
// multi-tenant API keys existed. It is also the bootstrap merchant used by
// single-tenant deployments.
var DefaultMerchantID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// Merchant is the tenant that owns API keys and payment data.
type Merchant struct {
	ID        uuid.UUID `db:"id" gorm:"column:id;primaryKey"`
	Name      string    `db:"name" gorm:"column:name;not null"`
	CreatedAt time.Time `db:"created_at" gorm:"column:created_at;not null"`
	UpdatedAt time.Time `db:"updated_at" gorm:"column:updated_at;not null"`
}

// TableName returns the merchants table name.
func (Merchant) TableName() string {
	return "merchants"
}
