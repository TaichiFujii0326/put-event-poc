.PHONY: build-EventReceiverFunction build-EventReceiverSimpleFunction build-SfnStarterFunction build-Job1Function build-Job2Function put-event

# sam build が呼び出すターゲット（関数名と一致させる）
build-EventReceiverFunction:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/receiver/

build-EventReceiverSimpleFunction:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/receiver-simple/

build-SfnStarterFunction:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/sfn-starter/

build-Job1Function:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/job1/

build-Job2Function:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/job2/

# ローカルからイベントを送信する
put-event:
	go run ./cmd/put-event/
