.PHONY: up down seed test bench fmt vet build frontend

up:
	docker compose up -d --build

down:
	docker compose down -v

seed:
	npm install
	npm run seed

test:
	go test ./... -count=1

bench:
	go run responsetime/main.go -test=login -workers=20 -n=500
	go run responsetime/main.go -test=transaction -workers=20 -n=500
	go run responsetime/main.go -test=monthly -workers=20 -n=500

build:
	go build ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

frontend:
	npx http-server UserInterface/
