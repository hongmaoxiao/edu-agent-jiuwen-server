// Command admin runs operator tasks against the database in DATABASE_URL.
//
//	admin create-operator <login> <display name>   # password is read from standard input, never from arguments
//	admin create-demo <login> <display name>       # demo account, its demo school and three personas; password on standard input
//	admin demo-reset <login>                       # remove everything opened during demos, keep the school and personas
//	admin import-content <file>                    # import courses, labs, guides and rubrics (seed/fde-content.json); safe to rerun
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/content"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	usage := fmt.Errorf("usage: admin create-operator|create-demo <login> <display name>  (password on standard input)\n       admin demo-reset <login>\n       admin import-content <file>")
	if len(args) == 0 {
		return usage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connect := func() (*pgxpool.Pool, error) {
		if os.Getenv("DATABASE_URL") == "" {
			return nil, fmt.Errorf("DATABASE_URL is required")
		}
		return pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	}
	open := func() (*account.Service, func(), error) {
		db, err := connect()
		if err != nil {
			return nil, nil, err
		}
		return account.New(db), db.Close, nil
	}
	switch {
	case (args[0] == "create-operator" || args[0] == "create-demo") && len(args) == 3:
		password, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && password == "" {
			return fmt.Errorf("read password from standard input: %w", err)
		}
		accounts, done, err := open()
		if err != nil {
			return err
		}
		defer done()
		create := accounts.CreateOperator
		if args[0] == "create-demo" {
			create = accounts.CreateDemo
		}
		created, err := create(ctx, args[1], args[2], strings.TrimRight(password, "\r\n"))
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s created (%s)\n", args[0], created.Login, created.ID)
	case args[0] == "demo-reset" && len(args) == 2:
		accounts, done, err := open()
		if err != nil {
			return err
		}
		defer done()
		if err := accounts.ResetDemo(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("demo %s reset\n", args[1])
	case args[0] == "import-content" && len(args) == 2:
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		export, err := content.Decode(raw)
		if err != nil {
			return err
		}
		db, err := connect()
		if err != nil {
			return err
		}
		defer db.Close()
		report, err := content.Import(ctx, db, export)
		if err != nil {
			return err
		}
		out, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(out))
	default:
		return usage
	}
	return nil
}
