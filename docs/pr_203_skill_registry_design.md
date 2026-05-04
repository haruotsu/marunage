# PR-203 Shared Skill Registry — Design Doc

## 対象範囲

`marunage skills install/list/search/update` と読み取り専用の Web UI を実装し、
HTTPS 経由で公開された SKILL.md パッケージを `~/.claude/skills/<name>/` に
取り込めるようにする。

このドキュメントは PR #37 (`feat/pr-203-skill-registry`) で実装された設計を
要約する。実装は以下に閉じる:

- `internal/skills/registry/` — レジストリプロトコルの型・HTTP クライアント・tar.gz 展開器・state 管理・Installer
- `internal/cli/skills.go` — `marunage skills` cobra サブコマンドツリー
- `internal/web/skills.go` + `templates/skills.html` — 読み取り専用 Web UI

PR-34（embedded skills）と PR-62（Web UI 基盤）は触らない。

## 1. レジストリプロトコル

### 1.1 ファイル配置

レジストリ発行者は HTTPS で以下を公開する:

```
<base>/index.json
<base>/skills/<name>/manifest.json
<base>/skills/<name>/<version>.tar.gz
```

### 1.2 `index.json` スキーマ

```json
{
  "schema_version": 1,
  "skills": [
    {"name": "marunage-source-jira", "latest": "0.2.0", "description": "Jira"}
  ]
}
```

`schema_version` は厳密一致を要求し、未来の拡張で v1 互換性を破る変更は
スキーマバンプを必須とする（`ErrUnsupportedSchema`）。

### 1.3 `manifest.json` スキーマ

```json
{
  "schema_version": 1,
  "name": "marunage-source-jira",
  "description": "Jira",
  "versions": [
    {
      "version": "0.2.0",
      "tarball_url": "https://example/skills/.../0.2.0.tar.gz",
      "sha256": "<hex digest>",
      "size_bytes": 1234,
      "published_at": "2026-01-15T00:00:00Z"
    }
  ]
}
```

versions は新しい順。`Find("")` で先頭を返し、明示版指定がない場合は
`latest` を選ぶ。`name` / `version` / `tarball_url` / `sha256` のいずれかが
空なら `ErrManifestMalformed`。

### 1.4 tarball レイアウト

公式コンベンション: `<name>/SKILL.md` を含む単一トップレベルディレクトリ。
extractor は `<name>/` プレフィックスを strip して dest 直下に配置する。

## 2. 整合性検証

- **sha256**: `manifest.json` の digest と DL バイト列を比較 → mismatch は `ErrIntegrity`
- **schema_version**: 厳密一致、不一致は `ErrUnsupportedSchema`
- **scheme allowlist**: `http` / `https` のみ許可。`file://` 等は `ErrInsecureRegistry`
- **size cap**:
  - HTTP body: `MaxBodyBytes` (default 8 MiB)
  - tar 解凍合計: `MaxTarBytes` (default 64 MiB)、超過は `ErrTarTooLarge`
- **path traversal**: `..` / 絶対パス / シンボリックリンクは `ErrUnsafeTarPath`
- **dest symlink**: dest が symlink の場合 rename 対象外として `ErrUnsafeTarPath`
- **atomic replace**: 一時ディレクトリへ展開 → `os.Rename` で旧木とまとめて差し替え
- **state file**: `~/.claude/skills/.marunage-registry.json` に
  `{name, version, source, sha256, updated_at}` を 0600 で記録

## 3. PR-34 との衝突回避

`marunage-triage` / `marunage-execute` / `marunage-reflect` は PR-34 で
`go:embed` 同梱しているため、レジストリ install は **既定で拒否** する
（`ErrEmbeddedConflict`）。明示的に上書きする場合のみ
`--allow-embedded-override` を要求する。

state file は embedded skill を記録しない（`marunage setup --skills` は
state を書かない）ので、`skills list` / `update` は registry 経由で導入された
skill のみを対象とする。

## 4. CLI

- `marunage skills install <name> [--version <ver>] [--registry <url>] [--allow-embedded-override]`
- `marunage skills list` — state file から表示
- `marunage skills search [query] [--registry <url>]` — index.json をフェッチ
- `marunage skills update [name] [--registry <url>]` — 名前指定なしなら state×index 比較で差分を検出

レジストリ URL は `--registry` または環境変数
`MARUNAGE_SKILLS_REGISTRY_URL` のいずれかで明示する。**ハードコード default は持たない**
（鮮度の落ちたサードパーティ URL に黙って fetch しないため）。

## 5. Web UI

PR-62 の `internal/web/server.go` に以下を追加:

- `GET /skills` — `skills.html` で installed list を表示
- `GET /api/skills/installed` — JSON 版
- `GET /api/skills/registry?q=...` — 上流カタログをプロキシ。未設定時は 503

Mutating endpoint は **PR-203 では出さない**。CSRF / POST フローは別 PR。

## 6. 非ゴール

- パッケージ署名 (minisign) — フックポイントは `Version.SHA256` のみ。署名は将来 PR
- TUF 等のメタデータ整合性 — 将来検討
- レジストリへの publish クライアント — server 側のツール、本 PR の範囲外
- 双方向同期 — registry → local の片方向のみ

## 7. テスト戦略

t_wada TDD で以下を網羅:

- ParseIndex / ParseManifest 構造的検証
- HTTP client (httptest.Server fixture) — 200 / 4xx / 5xx / size cap / scheme guard
- ExtractTarball — 正常 / traversal / 絶対パス / symlink entry / dest symlink / oversize / replace
- State file — 不在 / round-trip / upsert / 0600
- Installer end-to-end — happy path / 既存上書き / embedded conflict
- CLI — install / list / search / update against in-memory registry
- Web — HTML / JSON / 503 / non-mutating method

## 8. オープンクエスチョン

- レジストリのデフォルト URL（marunage 公式）はどこに置くか
- 署名の標準（minisign / sigstore / cosign）
- Skill のスキーマバージョンと SKILL.md 自身の `<!-- version: -->` の関係
- 複数レジストリ（チーム内ミラー）への同時参照
