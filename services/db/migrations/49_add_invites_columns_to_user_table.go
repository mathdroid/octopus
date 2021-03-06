package main

import (
	"fmt"

	"github.com/go-pg/migrations"
)

func init() {
	migrations.MustRegisterTx(func(db migrations.DB) error {
		fmt.Println("adding invites column to the users table...")
		_, err := db.Exec(`ALTER TABLE users ADD COLUMN invites_left INTEGER DEFAULT 0`)
		return err
	}, func(db migrations.DB) error {
		fmt.Println("dropping invites column from the users table...")
		_, err := db.Exec(`ALTER TABLE users DROP COLUMN invites_left`)
		return err
	})
}
