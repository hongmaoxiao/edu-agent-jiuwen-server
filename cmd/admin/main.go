// Command admin runs operator tasks against the database in DATABASE_URL.
//
//	admin create-operator <login> <display name>   # password is read from standard input, never from arguments
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"edu-agent-jiuwen-server/internal/account"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 || args[0] != "create-operator" {
		return fmt.Errorf("usage: admin create-operator <login> <display name>  (password on standard input)")
	}
	password, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && password == "" {
		return fmt.Errorf("read password from standard input: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	created, err := account.New(db).CreateOperator(ctx, args[1], args[2], strings.TrimRight(password, "\r\n"))
	if err != nil {
		return err
	}
	fmt.Printf("operator %s created (%s)\n", created.Login, created.ID)
	return nil
}
