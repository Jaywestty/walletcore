# Curl examples

Every command here was run against a locally running WalletCore server with a
real PostgreSQL database.

Start the server first:

```
docker compose up -d
```

Windows cmd:

```
set DATABASE_URL=postgres://walletcore:walletcore@localhost:5432/walletcore?sslmode=disable
set API_KEYS=svc:local-dev-key-0123456789abcdef0123456789ab
go run .\cmd\api
```

Linux or macOS:

```
DATABASE_URL=postgres://walletcore:walletcore@localhost:5432/walletcore?sslmode=disable \
API_KEYS=svc:local-dev-key-0123456789abcdef0123456789ab \
go run ./cmd/api
```

Set the key once per shell. Windows cmd:

```
set KEY=local-dev-key-0123456789abcdef0123456789ab
```

bash:

```
export KEY=local-dev-key-0123456789abcdef0123456789ab
```

All write requests need an `Idempotency-Key` header. Bodies live in
`examples/`, because cmd quoting of inline JSON breaks.

## Fund a wallet

Windows cmd:

```
curl -s -X POST http://localhost:8080/v1/funds -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: fund-1" -d @examples\fund.json
```

bash:

```
curl -s -X POST http://localhost:8080/v1/funds -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -H "Idempotency-Key: fund-1" -d @examples/fund.json
```

## Transfer with a fee

```
curl -s -X POST http://localhost:8080/v1/transfers -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: transfer-1" -d @examples\transfer.json
```

## Charge a fee

```
curl -s -X POST http://localhost:8080/v1/fees -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: fee-1" -d @examples\fee.json
```

## Request a withdrawal

```
curl -s -X POST http://localhost:8080/v1/withdrawals -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: wd-1" -d @examples\withdrawal.json
```

The response `id` is the withdrawal request journal id. Use it below.

## Finalize a withdrawal

```
curl -s -X POST http://localhost:8080/v1/withdrawals/<ID>/finalize -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: wd-1-fin" -d @examples\finalize.json
```

## Fail a withdrawal

```
curl -s -X POST http://localhost:8080/v1/withdrawals/<ID>/fail -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: wd-1-fail" -d @examples\fail.json
```

## Reverse a journal

```
curl -s -X POST http://localhost:8080/v1/journals/<ID>/reversal -H "Authorization: Bearer %KEY%" -H "Content-Type: application/json" -H "Idempotency-Key: rev-1" -d @examples\reversal.json
```

## Read balances

```
curl -s http://localhost:8080/v1/wallets/alice/USD -H "Authorization: Bearer %KEY%"
```

## Read a statement

```
curl -s "http://localhost:8080/v1/wallets/alice/USD/entries?limit=2" -H "Authorization: Bearer %KEY%"
```

## Read a journal

```
curl -s http://localhost:8080/v1/journals/<ID> -H "Authorization: Bearer %KEY%"
```

## Health

```
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
```
