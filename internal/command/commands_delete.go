// Port of tart's Commands/Delete.swift.
//go:build darwin

package command

import (
	"context"

	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
)

// DeleteCommand ports the Delete command.
type DeleteCommand struct {
	Names []string
}

func (c *DeleteCommand) Run(ctx context.Context) error {
	for _, name := range c.Names {
		if err := vmstorage.Remove(name); err != nil {
			return err
		}
	}
	return nil
}
