.PHONY: build-EventReceiverFunction put-event

# sam build が呼び出すターゲット（関数名と一致させる）
build-EventReceiverFunction:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/receiver/

# ローカルからイベントを送信する
put-event:
	go run ./cmd/put-event/
