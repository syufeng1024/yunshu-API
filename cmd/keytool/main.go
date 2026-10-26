// Command keytool encrypts a channel upstream key and, for local development
// only, writes the envelope into t_channel.upstream_key_enc.
//
//	CHANNEL_KEY_KEK=<base64-32B> keytool encrypt < plain.txt
//	ENV=local CHANNEL_KEY_KEK=... GW_WRITE_DSN=... keytool seed-channel -id 1 < plain.txt
//
// It prints neither the plaintext nor the envelope's ciphertext. The KEK is
// read from the environment and never from a flag, so it does not appear in
// the process list.
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/cryptobox"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "encrypt":
		err = runEncrypt(os.Stdin, os.Stdout)
	case "seed-channel":
		err = runSeedChannel(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		// Errors never include key material: cryptobox errors are value-free and
		// the SQL error does not contain the bound arguments.
		fmt.Fprintln(os.Stderr, "keytool: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  keytool encrypt < plaintext
  keytool seed-channel -id <channel_id> [-dsn <dsn>] < plaintext`)
}

func runEncrypt(r io.Reader, w io.Writer) error {
	box, err := cryptobox.FromEnv()
	if err != nil {
		return err
	}
	plaintext, err := readPlaintext(r)
	if err != nil {
		return err
	}
	envelope, err := box.Seal(plaintext)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, envelope)
	return err
}

func runSeedChannel(args []string) error {
	fs := flag.NewFlagSet("seed-channel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.Int64("id", 0, "channel id")
	dsn := fs.String("dsn", "", "MySQL DSN (defaults to GW_WRITE_DSN)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return errors.New("-id is required and must be positive")
	}
	// Upstream key write-back is a local development affordance. Refusing
	// outside ENV=local keeps a production operator from bypassing the control
	// plane's channel editor and its audit trail.
	if env := strings.TrimSpace(os.Getenv("ENV")); env != "local" {
		return fmt.Errorf("seed-channel is local-only (ENV=%q)", env)
	}
	box, err := cryptobox.FromEnv()
	if err != nil {
		return err
	}
	target := strings.TrimSpace(*dsn)
	if target == "" {
		target = os.Getenv("GW_WRITE_DSN")
	}
	if target == "" {
		return errors.New("no DSN: pass -dsn or set GW_WRITE_DSN")
	}
	plaintext, err := readPlaintext(os.Stdin)
	if err != nil {
		return err
	}
	envelope, err := box.Seal(plaintext)
	if err != nil {
		return err
	}
	mask := maskKey(string(plaintext))

	db, err := sql.Open("mysql", target)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()
	res, err := db.Exec(
		`UPDATE t_channel SET upstream_key_enc = ?, upstream_key_mask = ?, updated_at = UTC_TIMESTAMP(3)
		  WHERE id = ?`,
		[]byte(envelope), mask, *id,
	)
	if err != nil {
		return fmt.Errorf("update t_channel: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("channel %d not found", *id)
	}
	fmt.Fprintf(os.Stdout, "channel=%d updated key_mask=%s\n", *id, mask)
	return nil
}

func readPlaintext(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(data) > 64*1024 {
		return nil, errors.New("plaintext exceeds 64 KiB")
	}
	// Strip only the trailing newline an editor or echo adds; inner bytes are
	// significant for a key.
	data = []byte(strings.TrimRight(string(data), "\r\n"))
	if len(data) == 0 {
		return nil, errors.New("empty plaintext")
	}
	return data, nil
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "****" + key[len(key)-4:]
}
