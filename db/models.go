package db

import "time"

type User struct {
	Username string `gorm:"primary_key;unique;not null"`
}

type UserCredential struct {
	ID        uint      `gorm:"primarykey"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	Username  string    `gorm:"uniqueIndex:idx_uniq_credential_name,priority:1;not null"`
	PublicKey string    `gorm:"uniqueIndex:idx_uniq_credential_name,priority:2;not null"`
	User      User      `gorm:"foreignKey:Username"`
}
