package mihomo

import (
	"time"

	"github.com/AstraFoundry/KumoApp/internal/infrastructure/storage"
)

// Managed records which release Kumo downloaded. The version is the one the
// release announced when it was downloaded; Kumo does not ask the binary.
type Managed struct {
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installedAt"`
}

// ReadManaged loads the record at path. A missing or unreadable record
// gives the zero Managed, which has no version.
func ReadManaged(path string) Managed {
	var m Managed
	if _, err := storage.ReadJSON(path, &m); err != nil {
		return Managed{}
	}
	return m
}

// WriteManaged saves the record at path.
func WriteManaged(path string, m Managed) error {
	return storage.WriteJSON(path, m)
}
