---
title: "イベント駆動アーキテクチャを AWS SAM + Go で POC してユースケースを考察した"
emoji: "📨"
type: "tech"
topics: ["aws", "eventbridge", "sqs", "lambda", "sam", "go"]
published: false
---

## はじめに

バッチ基盤の更改に取り組む中で **Amazon EventBridge** を使い始めました。最初は cron 的なスケジュール実行の用途でしか使っていなかったのですが、調べていくうちにイベント駆動の機能があることを知りました。

最近、社内でもプロダクト間のデータ連携という話題が増えてきており、「このパターン、うちのユースケースに当てはめるとどうなるんだろう？」と気になったので POC しながら考察してみました。

本記事では AWS SAM と Go を使って、EventBridge の基本から SQS + DLQ を組み合わせた堅牢な構成まで実装します。

- **Part 1**: EventBridge → Lambda（シンプルな構成）
- **Part 2**: EventBridge → SQS → Lambda + DLQ（より堅牢な構成）

---

## EventBridge の基本概念

まず登場人物を整理します。

| 用語 | 説明 |
|------|------|
| **イベントバス** | イベントの受け口。デフォルトバスと、自分で作るカスタムバスがある |
| **ルール** | 「このパターンのイベントが来たら」という条件定義 |
| **ターゲット** | ルールにマッチしたイベントの転送先（Lambda、SQS、SNS など） |
| **PutEvents** | アプリケーションからイベントバスへイベントを送る API |

今回は以下のフローを実装します。

```
ローカルの Go CLI
  → PutEvents API
    → カスタムイベントバス (poc-event-bus)
      → ルール（source: "poc.order" にマッチ）
        → Lambda (Go) (poc-event-receiver)
          → CloudWatch Logs で確認
```

---

## プロジェクト構成

```
put-event-poc/
├── template.yaml          # SAM テンプレート
├── Makefile               # SAM が Go をビルドするために必要
├── go.mod
├── cmd/
│   ├── receiver/
│   │   └── main.go        # Lambda ハンドラー（イベントを受け取る側）
│   └── put-event/
│       └── main.go        # PutEvents を呼ぶ CLI（送る側）
```

---

## SAM テンプレート

```yaml
AWSTemplateFormatVersion: "2010-09-09"
Transform: AWS::Serverless-2016-10-31

Globals:
  Function:
    Runtime: provided.al2023
    Architectures:
      - arm64
    Timeout: 10

Resources:
  PocEventBus:
    Type: AWS::Events::EventBus
    Properties:
      Name: poc-event-bus

  EventReceiverFunction:
    Type: AWS::Serverless::Function
    Metadata:
      BuildMethod: makefile   # Go のビルドには Makefile が必要
    Properties:
      FunctionName: poc-event-receiver
      CodeUri: .
      Handler: bootstrap      # Go Lambda のバイナリ名は bootstrap 固定
      Events:
        EventBridgeTrigger:
          Type: EventBridgeRule
          Properties:
            EventBusName: !Ref PocEventBus
            Pattern:
              source:
                - "poc.order"
```

### Go Lambda で押さえるポイント

**① ランタイムは `provided.al2023`**
Go 用のマネージドランタイム `go1.x` は廃止されています。現在は `provided.al2023`（Amazon Linux 2023 のカスタムランタイム）を使います。

**② バイナリ名は `bootstrap` 固定**
カスタムランタイムでは、Lambda が起動時に `bootstrap` という名前のバイナリを探します。

**③ `BuildMethod: makefile` が必要**
SAM は Go を自動ビルドできないため、Makefile でビルドコマンドを定義します。

---

## Makefile

```makefile
.PHONY: build-EventReceiverFunction put-event

# sam build が呼び出すターゲット（SAMの関数名と一致させる）
build-EventReceiverFunction:
	GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/receiver/

# ローカルからイベントを送信する
put-event:
	go run ./cmd/put-event/
```

`build-EventReceiverFunction` というターゲット名は **SAM テンプレートの関数名の先頭に `build-` をつけたもの** でなければなりません。`sam build` が自動でこのターゲットを呼び出します。

クロスコンパイルのフラグ（`GOOS=linux GOARCH=arm64`）はローカルが Mac でも Lambda（Linux/ARM）向けにビルドするために必須です。

---

## Lambda ハンドラー（受け取る側）

```go
// cmd/receiver/main.go
package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

type OrderDetail struct {
	OrderID string     `json:"orderId"`
	UserID  string     `json:"userId"`
	Amount  int        `json:"amount"`
	Items   []LineItem `json:"items"`
}

type LineItem struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}

func handler(ctx context.Context, event events.CloudWatchEvent) error {
	log.Printf("source: %s", event.Source)
	log.Printf("detail-type: %s", event.DetailType)
	log.Printf("detail (raw): %s", string(event.Detail))

	var detail OrderDetail
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return err
	}

	log.Printf("orderId: %s, userId: %s, amount: %d",
		detail.OrderID, detail.UserID, detail.Amount)
	return nil
}

func main() {
	lambda.Start(handler)
}
```

### ポイント解説

**`events.CloudWatchEvent`** が EventBridge イベントの型です（歴史的経緯でこの名前になっています）。

```go
type CloudWatchEvent struct {
	Version    string          `json:"version"`
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	AccountID  string          `json:"account"`
	Time       time.Time       `json:"time"`
	Region     string          `json:"region"`
	Resources  []string        `json:"resources"`
	Detail     json.RawMessage `json:"detail"` // ← 任意のJSONが入る
}
```

`Detail` が `json.RawMessage`（生のJSONバイト列）なので、自分の構造体に `json.Unmarshal` して使います。

---

## PutEvents CLI（送る側）

```go
// cmd/put-event/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

type OrderDetail struct {
	OrderID string     `json:"orderId"`
	UserID  string     `json:"userId"`
	Amount  int        `json:"amount"`
	Items   []LineItem `json:"items"`
}

type LineItem struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}

func main() {
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("ap-northeast-1"))
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	client := eventbridge.NewFromConfig(cfg)

	detail := OrderDetail{
		OrderID: "order-001",
		UserID:  "user-abc",
		Amount:  3000,
		Items: []LineItem{
			{ProductID: "prod-1", Quantity: 2},
			{ProductID: "prod-2", Quantity: 1},
		},
	}

	// Detail は JSON 文字列として渡す必要がある
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		log.Fatalf("failed to marshal detail: %v", err)
	}

	input := &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{
			{
				EventBusName: aws.String("poc-event-bus"),
				Source:       aws.String("poc.order"),
				DetailType:   aws.String("OrderPlaced"),
				Detail:       aws.String(string(detailJSON)),
			},
		},
	}

	resp, err := client.PutEvents(ctx, input)
	if err != nil {
		log.Fatalf("PutEvents error: %v", err)
	}

	if resp.FailedEntryCount > 0 {
		for _, entry := range resp.Entries {
			if entry.ErrorCode != nil {
				log.Printf("failed: %s - %s", *entry.ErrorCode, *entry.ErrorMessage)
			}
		}
		log.Fatal("some entries failed")
	}

	fmt.Printf("Successfully sent %d event(s). EventID: %s\n",
		len(input.Entries), *resp.Entries[0].EventId)
}
```

### PutEvents の仕様で注意すること

| フィールド | 必須 | 説明 |
|-----------|------|------|
| `EventBusName` | ○ | カスタムバスの名前または ARN |
| `Source` | ○ | イベントの発生源（ルールの Pattern と一致させる） |
| `DetailType` | ○ | イベントの種類を示す任意の文字列 |
| `Detail` | ○ | **JSON を文字列化したもの**（`json.Marshal` → `string()` が必須） |

:::message alert
`Detail` はオブジェクトではなく **JSON 文字列** を渡します。Go では `json.Marshal` でバイト列にしてから `string()` でキャストします。
:::

**AWS SDK v2 では文字列フィールドのほとんどが `*string`（ポインタ）です。** `aws.String()` ヘルパーで包んでください。

---

## デプロイして動かす

### 1. 依存パッケージの取得

```bash
go mod tidy
```

### 2. SAM でビルド＆デプロイ

```bash
sam build
sam deploy --guided
```

`--guided` の対話入力例：

```
Stack Name: put-event-poc
AWS Region: ap-northeast-1
Confirm changes before deploy: y
Allow SAM CLI IAM role creation: y
Disable rollback: n
Save arguments to configuration file: y
```

### 3. イベントを送信

```bash
make put-event
```

成功すると以下のように出力されます：

```
Successfully sent 1 event(s). EventID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

### 4. CloudWatch Logs で確認

AWS コンソールで **CloudWatch → ロググループ → `/aws/lambda/poc-event-receiver`** を開きます。

```
source: poc.order
detail-type: OrderPlaced
detail (raw): {"orderId":"order-001","userId":"user-abc","amount":3000,...}
orderId: order-001, userId: user-abc, amount: 3000
```

---

## ハマりやすいポイントまとめ

### 1. ランタイムは `provided.al2023`（`go1.x` は廃止）

```yaml
# NG（廃止済み）
Runtime: go1.x

# OK
Runtime: provided.al2023
```

### 2. `BuildMethod: makefile` を忘れない

SAM は Go を自動ビルドしません。`Metadata.BuildMethod: makefile` がないと `sam build` でエラーになります。

### 3. Makefile のターゲット名は `build-{関数名}`

```makefile
# SAM テンプレートの関数名が EventReceiverFunction なら
build-EventReceiverFunction:
    GOOS=linux GOARCH=arm64 go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/receiver/
```

### 4. `Detail` は文字列にしてから渡す

```go
// NG: 構造体をそのまま渡せない（型エラー）
Detail: aws.String(detail) // コンパイルエラー

// OK
detailJSON, _ := json.Marshal(detail)
Detail: aws.String(string(detailJSON))
```

### 5. SDK v2 の文字列はポインタ

```go
// NG
Source: "poc.order"

// OK
Source: aws.String("poc.order")
```

---

---

# Part 2: SQS + DLQ を追加してより堅牢に

## Part 1 の問題点

Part 1 の構成では、Lambda の処理が失敗したとき EventBridge は**最大24時間リトライ**しますが、リトライ上限を超えるとイベントが消えます。また、どのイベントが失敗したかを後から確認する手段がありません。

```
EventBridge → Lambda
               ↑ リトライ上限を超えたらイベントが消える
               ↑ 失敗したイベントを後から確認できない
```

本番環境では「処理できなかったイベントを捨てたくない」ケースがほとんどです。SQS と DLQ（デッドレターキュー）を挟むことでこれを解決します。

## SQS + DLQ を挟んだ構成

```
EventBridge → SQS（メインキュー） → Lambda
                ↓ 3回失敗したら
              DLQ（デッドレターキュー）
```

**何が嬉しいか：**

| | Part 1（EventBridgeのみ） | Part 2（SQS追加） |
|---|---|---|
| Lambda 失敗時 | イベントが消える | SQS に残って自動再試行 |
| 再試行回数 | 0回 | 最大3回（設定可能） |
| 3回失敗し続けた場合 | 消える | DLQ に移動して原因調査できる |
| 大量イベント | Lambda が一気に起動 | SQS がバッファして流量を調整 |

---

## SAM テンプレートの変更点

```yaml
Resources:
  PocEventBus:
    Type: AWS::Events::EventBus
    Properties:
      Name: poc-event-bus

  # デッドレターキュー
  EventDLQ:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: poc-event-dlq

  # メインキュー
  EventQueue:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: poc-event-queue
      RedrivePolicy:
        deadLetterTargetArn: !GetAtt EventDLQ.Arn
        maxReceiveCount: 3  # 3回失敗したらDLQへ

  # EventBridgeがSQSにメッセージを送るためのポリシー
  EventQueuePolicy:
    Type: AWS::SQS::QueuePolicy
    Properties:
      Queues:
        - !Ref EventQueue
      PolicyDocument:
        Version: "2012-10-17"
        Statement:
          - Effect: Allow
            Principal:
              Service: events.amazonaws.com
            Action: sqs:SendMessage
            Resource: !GetAtt EventQueue.Arn
            Condition:
              ArnEquals:
                aws:SourceArn: !GetAtt OrderEventRule.Arn

  # EventBridgeルール（ターゲットをSQSに変更）
  OrderEventRule:
    Type: AWS::Events::Rule
    Properties:
      EventBusName: !Ref PocEventBus
      EventPattern:
        source:
          - "poc.order"
      Targets:
        - Id: SQSTarget
          Arn: !GetAtt EventQueue.Arn

  # Lambda（SQSトリガーに変更）
  EventReceiverFunction:
    Type: AWS::Serverless::Function
    Metadata:
      BuildMethod: makefile
    Properties:
      FunctionName: poc-event-receiver
      CodeUri: .
      Handler: bootstrap
      Policies:
        - SQSPollerPolicy:
            QueueName: !GetAtt EventQueue.QueueName
      Events:
        SQSTrigger:
          Type: SQS
          Properties:
            Queue: !GetAtt EventQueue.Arn
            BatchSize: 1
```

### 変更のポイント

**① EventBridgeルールのターゲットが Lambda → SQS に変わった**

Part 1 では SAM の `Events.EventBridgeRule` で Lambda を直接トリガーしていましたが、Part 2 では `AWS::Events::Rule` を明示的に書いて SQS をターゲットにしています。

**② `EventQueuePolicy` が必要**

EventBridge が SQS にメッセージを送るには、SQS 側で「EventBridge からの `sqs:SendMessage` を許可する」ポリシーを設定する必要があります。これを忘れるとイベントが SQS に届きません。

**③ Lambda のトリガーが SQS になった**

`Events.SQSTrigger` の `Type: SQS` を設定すると、SAM が自動で Lambda の `EventSourceMapping`（SQS → Lambda のポーリング設定）と必要な IAM ポリシーを作成します。

---

## Lambda ハンドラーの変更点

SQS 経由になると、Lambda が受け取るイベントの型が変わります。

```go
// cmd/receiver/main.go
func handler(ctx context.Context, sqsEvent events.SQSEvent) error {
    for _, record := range sqsEvent.Records {
        // SQSのBodyにEventBridgeのイベントがJSON文字列として格納されている
        var ebEvent events.CloudWatchEvent
        if err := json.Unmarshal([]byte(record.Body), &ebEvent); err != nil {
            log.Printf("failed to unmarshal EventBridge event: %v", err)
            return err
        }

        log.Printf("source: %s", ebEvent.Source)
        log.Printf("detail-type: %s", ebEvent.DetailType)

        var detail OrderDetail
        if err := json.Unmarshal(ebEvent.Detail, &detail); err != nil {
            log.Printf("failed to unmarshal order detail: %v", err)
            return err
        }

        log.Printf("orderId: %s, userId: %s, amount: %d",
            detail.OrderID, detail.UserID, detail.Amount)
    }
    return nil
}
```

**Part 1 との違い：**

| | Part 1 | Part 2 |
|---|---|---|
| 引数の型 | `events.CloudWatchEvent` | `events.SQSEvent` |
| イベントの取り出し方 | そのまま使う | `record.Body` を `json.Unmarshal` |
| 複数イベント | 1件ずつ | `sqsEvent.Records` をループ |

**なぜ2段階の `json.Unmarshal` が必要か：**

SQS のメッセージ構造はこうなっています。

```json
{
  "Records": [
    {
      "body": "{\"source\":\"poc.order\", \"detail-type\":\"OrderPlaced\", \"detail\":{...}}"
    }
  ]
}
```

`body` の中に EventBridge のイベントが **JSON 文字列**として入っているため、まず `record.Body` を `CloudWatchEvent` に Unmarshal し、次に `ebEvent.Detail` を `OrderDetail` に Unmarshal する2段階が必要です。

:::message alert
`handler` でエラーを返すと、Lambda は処理失敗とみなして SQS にメッセージを戻します。これが `maxReceiveCount` を超えると DLQ に移動します。つまり**エラーを握りつぶさないことが DLQ を機能させる条件**です。
:::

---

## デプロイして動かす

```bash
sam build
sam deploy --capabilities CAPABILITY_NAMED_IAM
make put-event
```

CloudWatch Logs で同じログが出ていれば成功です。

---

## DLQ を確認してみる

あえて Lambda を失敗させて DLQ に積まれることを確認してみましょう。

`cmd/receiver/main.go` のハンドラーを一時的に常にエラーを返すように変更してビルドし直します。

```go
func handler(ctx context.Context, sqsEvent events.SQSEvent) error {
    return fmt.Errorf("intentional error for testing DLQ")
}
```

3回リトライ後、AWSコンソールの **SQS → `poc-event-dlq` → メッセージを送受信** からメッセージが届いていることを確認できます。

---

## クリーンアップ

```bash
sam delete
```

---

## まとめ

| | Part 1 | Part 2 |
|---|---|---|
| 構成 | EventBridge → Lambda | EventBridge → SQS → Lambda + DLQ |
| 失敗時 | イベント消失 | 自動再試行 → DLQ に保管 |
| 向いてるケース | プロトタイプ・検証 | 本番環境 |

SQS と DLQ を追加するだけでシステムの信頼性が大きく向上します。「イベントを絶対に落としたくない」要件がある場合は Part 2 の構成をベースにするのがおすすめです。

---

# 考察：EventBridge が本領を発揮するユースケース

## EventBridge が向いているのはどんなとき？

連携先が1つで今後も増えない見込みなら **API 呼び出しで十分**です。

EventBridge が本領を発揮するのは、**「あるイベントをきっかけに複数の処理が走る」かつ「今後も処理が増えることが予想される」とき**です。

| | API 呼び出し | EventBridge |
|---|---|---|
| 連携先が増えたとき | 送信側が対応必須 | 受け取る側が追加するだけ |
| 連携先の把握 | 送信側がすべて知っている必要がある | 送信側は知らなくていい |
| 連携先のリリースタイミング | 送信側と調整必要 | 独立してリリースできる |

---

## ユースケース① 給与システム

給与確定をきっかけに複数プロダクトへ通知が必要なケースです。

```
給与確定イベント
  ├→ 社会保険システム：保険料の計算・更新
  ├→ 人材管理システム：給与情報の反映
  ├→ 退職者管理システム：給与停止フラグの確認
  └→ 年末調整：源泉徴収データの更新
```

給与チームは「給与が確定した」というイベントを投げるだけで、各プロダクトは独立して動きます。新しいプロダクトが給与データと連携したくなっても、**給与チームのコードは一切変わりません。**

---

## ユースケース② 人事システム

人事イベントは会社全体に影響するため、EventBridge との相性が特に良いです。

```
入社イベント
  ├→ 社会保険システム：加入手続き
  ├→ 給与：給与設定
  ├→ Slack：アカウント作成
  ├→ GitHub：組織への招待
  └→ 入社研修：スケジュール登録

退職イベント
  ├→ 社会保険システム：喪失手続き
  ├→ 給与：最終給与計算
  ├→ Slack：アカウント無効化
  └→ GitHub：組織から除外

異動イベント
  ├→ 給与：給与変更
  ├→ 権限管理：アクセス権変更
  └→ 人材管理システム：情報更新
```

現状、これらを手動でやっていたり各システムが人事DBをポーリングしているケースが多いです。EventBridge にすると人事システムはイベントを投げるだけで、各システムが自律的に動きます。

---

## ユースケース③ EC システム

注文確定をきっかけに複数の処理が並行して走るパターンです。

```
注文確定イベント
  ├→ メール送信：注文確認メール
  ├→ 在庫管理：在庫の引き当て
  ├→ 配送：配送手配
  ├→ ポイント：ポイント付与
  └→ 分析基盤：売上データ記録
```

注文サービスは「注文が確定した」だけを知っていればよく、「誰が何をするか」は関知しません。

---

## 共通する設計思想：抽象に依存せよ

3つのユースケースに共通するのは、**送信側が「何が起きたか（イベント）」だけを発信し、「誰がどう処理するか」を知らない**という構造です。

```
// API 呼び出し：送信側が受信側の具体的な実装に依存
給与システム → 社会保険API（具体）

// EventBridge：両者がイベントスキーマ（抽象）に依存
給与システム → イベントスキーマ ← 社会保険システム
```

これはクリーンアーキテクチャの DIP（依存性逆転の原則）が掲げる「**具体ではなく抽象に依存せよ**」という考え方と共通しています。

ただし「依存性が逆転している」わけではありません。EventBridge でやっていることは「両者が抽象（イベントスキーマ）に依存することで直接的な結合をなくす」Pub-Sub パターンです。DIP と共通しているのは「具体に依存するな、抽象に依存せよ」という部分だけです。

コードレベルの設計原則がインフラレベルでも同じ形で現れるのが面白い発見でした。

---

## 直列実行が必要なら Step Functions と組み合わせる

EventBridge 単体では順序を保証しません。直列実行が必要なジョブは **Step Functions** に任せるのが素直な設計です。

### パターン①：シンプルな直列実行

```
PutEvents
  → EventBridge
      ├→ Step Functions（Job1 → Job2 の直列保証）
      └→ Lambda（単発ジョブ）
```

ジョブの件数が少なく、バーストが起きない場合はこれで十分です。

### パターン②：負荷平準化 + 直列実行

大量のイベントが短時間に発生する可能性がある場合は、SQS を挟んでバッファします。

```
PutEvents
  → EventBridge
      └→ SQS（バッファ・流量制御）
          └→ Lambda（Step Functions を起動するだけ）
              └→ Step Functions
                    → Job1
                    → Job2
```

| | パターン① | パターン② |
|---|---|---|
| 直列実行の保証 | ✅ | ✅ |
| 失敗時のリトライ | ✅（Step Functions） | ✅（SQS + Step Functions） |
| 負荷平準化 | ❌ | ✅ |
| 構成のシンプルさ | ✅ | △ |

EventBridge はルーティング、SQS はバッファ、Step Functions は順序制御、と役割を明確に分けることで、それぞれの強みを活かせます。

---

## クリーンアップ

```bash
sam delete
```

---

## 参考

- [Amazon EventBridge とは - AWS ドキュメント](https://docs.aws.amazon.com/ja_jp/eventbridge/latest/userguide/eb-what-is.html)
- [Amazon SQS デッドレターキュー - AWS ドキュメント](https://docs.aws.amazon.com/ja_jp/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html)
- [AWS Step Functions とは - AWS ドキュメント](https://docs.aws.amazon.com/ja_jp/step-functions/latest/dg/welcome.html)
- [AWS SAM で Go Lambda をビルドする - AWS ドキュメント](https://docs.aws.amazon.com/ja_jp/serverless-application-model/latest/developerguide/building-custom-runtimes.html)
- [aws-lambda-go - GitHub](https://github.com/aws/aws-lambda-go)
- [AWS SDK for Go v2 - GitHub](https://github.com/aws/aws-sdk-go-v2)
