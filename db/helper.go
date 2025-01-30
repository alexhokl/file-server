package db

import (
	"gorm.io/gorm"
)

func Migrate(db *gorm.DB) error {
	err := db.AutoMigrate(&User{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&UserCredential{})
	if err != nil {
		return err
	}
	return nil
}
