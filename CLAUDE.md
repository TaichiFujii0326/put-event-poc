# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## リポジトリ概要

Amazon EventBridgeのPutEvents APIを検証するPOC。バッチ基盤更改でEventBridgeを使い始め、イベント駆動のプロダクト間データ連携への応用を考察することが目的。

- GitHubリポジトリ: https://github.com/TaichiFujii0326/put-event-poc
- 技術スタック: Go 1.22 / AWS SAM / EventBridge / SQS / Lambda
- リージョン: ap-northeast-1

## Commands

```bash
# AWSにデプロイ（SQSあり構成）
sam build && sam deploy

# AWSにデプロイ（シンプル構成）
sam build --template template-simple.yaml && sam deploy --template template-simple.yaml

# イベントを送信（デプロイ済み環境に対して実行）
make put-event
```

## アーキテクチャ

EventBridgeのPutEvents APIを検証するPOCで、2つの構成がある。

**SQSあり構成 (`template.yaml`)**
```
[put-event スクリプト] → EventBridge (poc-event-bus) → ルール (source: poc.order) → SQS → Lambda
                                                                                      ↓ 3回失敗
                                                                                     DLQ
```

**シンプル構成 (`template-simple.yaml`)**
```
[put-event スクリプト] → EventBridge (poc-event-bus-simple) → ルール (source: poc.order) → Lambda
```

## コード構成

- `cmd/put-event/` — イベント送信スクリプト。ローカルから `PutEvents` APIを叩く
- `cmd/receiver/` — SQSトリガーのLambda。SQSのBodyからEventBridgeイベントをUnmarshalして処理する
- `cmd/receiver-simple/` — EventBridgeから直接トリガーされるLambda。`events.CloudWatchEvent` を直接受け取る

## Lambdaのビルド

`sam build` はMakefileの `build-{関数名}` ターゲットを呼び出す（`BuildMethod: makefile`）。`ARTIFACTS_DIR` はSAMが自動でセットする環境変数で、成果物は `bootstrap` という名前で出力する必要がある。

## デプロイ設定

`samconfig.toml` にデフォルト設定が定義されている。スタック名は `put-event-poc`、リージョンは `ap-northeast-1`。
