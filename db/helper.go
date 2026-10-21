package db

import (
	"gorm.io/gorm"
)

// usersFkName is the name of the foreign key on the credential table's
// username column referencing the users table. AutoMigrate does not create
// constraints for this relation, so it is ensured here explicitly.
const usersFkName = "fk_user_credentials_user"

func Migrate(db *gorm.DB) error {
	err := db.AutoMigrate(&User{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&UserCredential{})
	if err != nil {
		return err
	}
	if !db.Migrator().HasConstraint(&UserCredential{}, usersFkName) {
		err = db.Migrator().CreateConstraint(&UserCredential{}, usersFkName)
		if err != nil {
			return err
		}
	}
	return nil
}
