// Package publish copies a built site to where it's served from: a Bunny
// storage zone, with a pull zone in front of it, or a web server reached by
// rsync.
package publish

import (
	"context"
	"database/sql"
	"strings"
)

// The settings that say where a library's site is published, as they are
// named in the database's setting table.  Bunny's two secrets, the storage
// zone's password and the account's API key, are not settings: they are
// read from the environment, and never kept in the database.
const (
	SettingRsyncTarget  = "publish.rsync.target"
	SettingRsyncCommand = "publish.rsync.command"

	SettingBunnyZone       = "publish.bunny.zone"
	SettingBunnyStorageURL = "publish.bunny.storage_url"
	SettingBunnyPullZoneID = "publish.bunny.pull_zone_id"

	// The hostnames to purge the CDN's cache on, separated by spaces.
	// Without it, they are the pull zone's own.
	SettingBunnyPurgeHosts = "publish.bunny.purge_hosts"
)

// SettingPrefix begins the name of every setting that is about publishing.
const SettingPrefix = "publish."

// Config is where a library's site is published.
type Config struct {
	// RsyncTarget is where rsync copies the site to, like
	// "user@example.com:/var/www/photos/", or empty if it isn't
	// published that way.  RsyncCommand is the rsync to run.
	RsyncTarget  string
	RsyncCommand string

	// BunnyZone is the storage zone the site is uploaded to, or empty
	// if it isn't published that way.
	BunnyZone       string
	BunnyStorageURL string
	BunnyPullZoneID string
	BunnyPurgeHosts []string
}

// Targets are the ways the site is published: "rsync", "bunny", or both.
func (c Config) Targets() []string {
	var targets []string
	if c.RsyncTarget != "" {
		targets = append(targets, "rsync")
	}
	if c.BunnyZone != "" {
		targets = append(targets, "bunny")
	}
	return targets
}

// LoadConfig reads the publishing settings from a jiggle database.
func LoadConfig(ctx context.Context, db *sql.DB) (Config, error) {
	c := Config{RsyncCommand: "rsync"}
	rows, err := db.QueryContext(ctx, `SELECT name, value FROM setting WHERE name LIKE ?`, SettingPrefix+"%")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return c, err
		}
		switch name {
		case SettingRsyncTarget:
			c.RsyncTarget = value
		case SettingRsyncCommand:
			c.RsyncCommand = value
		case SettingBunnyZone:
			c.BunnyZone = value
		case SettingBunnyStorageURL:
			c.BunnyStorageURL = value
		case SettingBunnyPullZoneID:
			c.BunnyPullZoneID = value
		case SettingBunnyPurgeHosts:
			c.BunnyPurgeHosts = strings.Fields(value)
		}
	}
	return c, rows.Err()
}
