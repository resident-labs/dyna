package contract

import "time"

type BundleVersion int

const BundleVersion1 BundleVersion = 1

type BundleSource struct {
	Commit       string `json:"commit"` // the v1-final commit
	Host         string `json:"host"`
	BackupFile   string `json:"backupFile"` // the final v1 backup
	BackupSHA256 string `json:"backupSha256"`
}
