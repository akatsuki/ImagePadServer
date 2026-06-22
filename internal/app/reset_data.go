package app

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"imagepadserver/internal/settings"
)

// ResetData deletes the entire ImagePadServer data directory after confirmation.
// Pass args from os.Args[2:]. When --yes is set the confirmation prompt is skipped.
func ResetData(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reset-data", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir := settings.Dir()
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "nothing to delete: %s does not exist\n", dir)
		return nil
	}

	if !*yes {
		fmt.Fprintf(out, "This will permanently delete ALL ImagePadServer data:\n  %s\nType \"yes\" to confirm: ", dir)
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			return errors.New("aborted")
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to delete %s: %w", dir, err)
	}
	fmt.Fprintf(out, "deleted: %s\n", dir)
	return nil
}
