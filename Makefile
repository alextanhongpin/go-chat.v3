run:
	open http://localhost:9090?from=alice\&to=bob
	go run *.go -addr=:9090

run2:
	open http://localhost:8080?from=bob\&to=alice
	go run *.go -addr=:8080


up:
	docker compose up -d


down:
	docker compose down
