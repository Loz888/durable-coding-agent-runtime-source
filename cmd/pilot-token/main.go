// Operator command: displays a NEW secret once to its own terminal, never takes secrets in argv.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"os"
	"runtime-app/pilotapi"
	"time"
)

func main() {
	principal := flag.String("principal", "", "trusted principal")
	team := flag.String("team", "", "trusted team")
	revoke := flag.String("revoke-hash", "", "revoke a token by its SHA-256, no raw token required")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dsn := os.Getenv("PILOT_ADMIN_DATABASE_URL")
	if dsn == "" {
		log.Fatal("operator migration/identity database URL required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal("database unavailable")
	}
	defer pool.Close()
	if *revoke != "" {
		if len(*revoke) != 64 {
			log.Fatal("invalid digest")
		}
		if _, err = hex.DecodeString(*revoke); err != nil {
			log.Fatal("invalid digest")
		}
		_, err = pool.Exec(ctx, "UPDATE pilot.tokens SET revoked_at=clock_timestamp() WHERE token_hash=$1", *revoke)
		if err != nil {
			log.Fatal("revocation failed")
		}
		fmt.Println("revoked")
		return
	}
	if *principal == "" || *team == "" || len(*principal) > 100 || len(*team) > 100 {
		log.Fatal("bounded principal and team required")
	}
	var data [32]byte
	if _, err = rand.Read(data[:]); err != nil {
		log.Fatal("random generation failed")
	}
	token := hex.EncodeToString(data[:])
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatal("database unavailable")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO pilot.teams(team,max_pending,per_minute) VALUES($1,10,30) ON CONFLICT DO NOTHING", *team); err != nil {
		log.Fatal("team configuration failed")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO pilot.tokens(token_hash,principal,team,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '30 days')", pilotapi.HashToken(token), *principal, *team); err != nil {
		log.Fatal("token provisioning failed")
	}
	if tx.Commit(ctx) != nil {
		log.Fatal("commit failed")
	}
	fmt.Println(token)
}
