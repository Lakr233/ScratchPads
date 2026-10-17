.PHONY: fmt lint test up down logs pin

fmt:
	gofmt -w .
	npx --yes prettier@3.3.3 --write "web/**/*.{js,css,html}" scripts/*.mjs

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	npx --yes prettier@3.3.3 --check "web/**/*.{js,css,html}"
	node scripts/check-i18n.mjs

test:
	go test ./...

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f scratchpads

pin:
	docker compose exec scratchpads scratchpads pin
