run:
	open http://localhost:9090?chat_id=chat-1\&user_id=alice
	go run *.go -addr=:9090

run2:
	open http://localhost:8080?chat_id=chat-1\&user_id=bob
	go run *.go -addr=:8080


up:
	docker compose up -d


down:
	docker compose down
