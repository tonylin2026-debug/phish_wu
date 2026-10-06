# phish_wu

以 [Gophish](https://github.com/gophish/gophish) 0.12.1 為基礎的分支，供企業內部進行**授權的**釣魚演練與資安意識訓練使用。

與上游最主要的差異是：**開啟郵件、點擊連結、開啟附件三個動作各自獨立追蹤**，同一封信內三者可同時成立且互不干擾。

> ⚠️ 本工具僅供你有明確書面授權的對象使用。請在演練前確認你的組織已完成內部核准流程。

---

## 目錄

- [與上游 Gophish 的差異](#與上游-gophish-的差異)
- [系統架構](#系統架構)
- [建置](#建置)
- [設定檔](#設定檔)
- [資料庫](#資料庫)
- [首次啟動](#首次啟動)
- [正式部署](#正式部署)
- [三動作追蹤設定](#三動作追蹤設定)
- [樣板變數](#樣板變數)
- [REST API](#rest-api)
- [使用者回報（IMAP）](#使用者回報imap)
- [Webhooks](#webhooks)
- [安全性注意事項](#安全性注意事項)
- [開發與測試](#開發與測試)
- [授權](#授權)

---

## 與上游 Gophish 的差異

### 1. 三個動作獨立追蹤

上游的 `Result.Status` 是一個**有序狀態機**：

```
Email Sent → Email Opened → Clicked Link → Submitted Data
```

因為只有單一欄位，一個收件者永遠只能被記錄在「最遠抵達的那一階」。點了連結之後，「開啟郵件」就不再被記錄；開啟附件更是完全不在這條序列上。

本分支為每個動作建立**獨立的事實欄位**：

| 欄位 | 意義 |
|---|---|
| `email_opened` | 信件內的追蹤像素被載入 |
| `attachment_opened` | 附件內的遠端資源被載入 |
| `clicked_link` | 落地頁連結被點擊 |
| `submitted_data` | 落地頁表單被提交 |

`Status` 保留原本「最遠抵達步驟」的語意，所有既有的檢視與匯出行為不變。

三個動作是三個可能同時抵達的 HTTP 請求，因此所有寫入都改為**只更新指定欄位**的 targeted UPDATE，狀態機的守衛條件放進 `WHERE` 子句成為單一原子語句，避免併發請求互相覆蓋。

### 2. 開啟郵件的統計改為雙欄並列

| 統計欄位 | 意義 |
|---|---|
| `opened` | **實測值**：追蹤像素確實被載入 |
| `engaged` | **觸及值**：開信 **或** 開附件 **或** 點擊 **或** 提交 |

郵件用戶端普遍阻擋遠端圖片，所以實測開信率會低估。上游的作法是「點擊了就推論他開過信」，會把推論值混進實測值。本分支兩個數字都提供，由看報表的人自行判斷。

### 3. MySQL 8 相容性修復

上游在**預設設定**的 MySQL 8 上無法初始化資料庫，也無法建立活動。詳見[資料庫](#資料庫)一節。

---

## 系統架構

```
gophish.go
 │
 ├── 管理伺服器 (admin)     預設 127.0.0.1:3333 (TLS)
 │    ├── 網頁介面          /、/campaigns、/groups、/templates、
 │    │                    /landing_pages、/sending_profiles、/settings
 │    │                    /users、/webhooks（需 admin 權限）
 │    ├── REST API          /api/**（API Key 驗證）
 │    └── 背景 worker       每 60 秒輪詢待寄郵件 → SMTP
 │
 ├── 釣魚伺服器 (phish)     預設 0.0.0.0:80
 │    ├── /track                  開啟郵件（追蹤像素）
 │    ├── /track/attachment       開啟附件
 │    ├── /report                 使用者回報
 │    ├── /robots.txt             Disallow: /
 │    └── /{path}                 落地頁（GET=點擊、POST=提交）
 │
 └── IMAP 監控              每個使用者一個 goroutine，輪詢信箱收取被回報的信
```

兩個伺服器可以用 `--mode` 拆開部署到不同主機。

---

## 建置

### 需求

| 項目 | 版本 | 說明 |
|---|---|---|
| Go | 1.21 以上 | CI 測試 1.21 / 1.22 / 1.23 |
| C 編譯器 | gcc 或同等 | **必要**，`mattn/go-sqlite3` 需要 CGO |
| Node.js | 選擇性 | 僅在要重新壓縮前端資源時需要 |

### 編譯

```bash
git clone https://github.com/tonylin2026-debug/phish_wu.git
cd phish_wu
CGO_ENABLED=1 go build -o gophish .
```

Windows 上需要先安裝 MinGW-w64（例如透過 MSYS2 的 `mingw-w64-ucrt-x86_64-gcc`），並確認 `gcc --version` 有輸出，否則 sqlite3 driver 無法編譯。

### 執行時需要的檔案

gophish **不是單一執行檔**，它會從**工作目錄**讀取以下內容，部署時必須一併帶上：

```
gophish              執行檔
VERSION              缺少此檔會直接啟動失敗
config.json          設定檔
db/                  資料庫 migration（db_sqlite3/ 與 db_mysql/）
templates/           管理介面的 HTML 樣板
static/
  ├── css/dist/      管理介面樣式
  ├── js/dist/       管理介面腳本
  ├── js/src/vendor/ckeditor/
  ├── images/        含追蹤像素 pixel.png
  ├── font/
  └── db/geolite2-city.mmdb   GeoIP 資料庫（約 38 MB）
```

### 重新建置前端（選擇性）

```bash
yarn install --frozen-lockfile --ignore-scripts
npx gulp
```

> 前端相依樹是 2019 年版本，`npm install` 會**忽略 `yarn.lock`** 重新解析版本範圍，拉下大量無人維護的傳遞相依套件。建議使用 `yarn --frozen-lockfile`，或在容器／VM 等隔離環境中執行。
>
> 若不執行此步驟，`static/js/dist/app/*.min.js` 會是未壓縮的原始碼——功能完全相同，只是檔案較大。

---

## 設定檔

`config.json` 完整欄位：

```json
{
  "admin_server": {
    "listen_url": "127.0.0.1:3333",
    "use_tls": true,
    "cert_path": "gophish_admin.crt",
    "key_path": "gophish_admin.key",
    "csrf_key": "",
    "trusted_origins": [],
    "allowed_internal_hosts": []
  },
  "phish_server": {
    "listen_url": "0.0.0.0:80",
    "use_tls": false,
    "cert_path": "example.crt",
    "key_path": "example.key"
  },
  "db_name": "sqlite3",
  "db_path": "gophish.db",
  "db_sslca_path": "",
  "migrations_prefix": "db/db_",
  "contact_address": "",
  "logging": {
    "filename": "",
    "level": ""
  }
}
```

### 欄位說明

| 欄位 | 說明 |
|---|---|
| `admin_server.listen_url` | 管理介面位址。**強烈建議維持 `127.0.0.1`**，對外請用反向代理 |
| `admin_server.use_tls` | 為 `true` 且憑證檔不存在時，會自動產生有效期 10 年的自簽憑證 |
| `admin_server.csrf_key` | CSRF 簽章金鑰。留空則每次啟動隨機產生，重啟後所有表單 token 失效 |
| `admin_server.trusted_origins` | 位於反向代理後方且對外網域不同時，需在此列出該網域，否則 CSRF 檢查會擋下登入 |
| `admin_server.allowed_internal_hosts` | SSRF 白名單，見下方說明 |
| `phish_server.listen_url` | 釣魚伺服器位址。預設 `0.0.0.0:80` 會對整個網路開放 |
| `db_name` | `sqlite3` 或 `mysql` |
| `db_path` | SQLite 為檔案路徑；MySQL 為連線字串 |
| `db_sslca_path` | MySQL TLS 連線的 CA 憑證路徑 |
| `migrations_prefix` | migration 目錄前綴，實際路徑為 `<prefix><db_name>` |
| `contact_address` | 演練聯絡信箱。會出現在信件的 `X-Gophish-Contact` 標頭與透明度回應中 |
| `logging.filename` | 留空只輸出到 stderr；填寫則同時寫入檔案 |
| `logging.level` | `debug` / `info` / `error` / `fatal`，留空為 `info` |

### SSRF 防護

`allowed_internal_hosts` 控制 gophish 對外連線（SMTP、IMAP、webhook、匯入網站）可以抵達的位址：

- **留空**（預設）：僅封鎖 `169.254.0.0/16`（雲端 metadata 端點）
- **有設定值**：封鎖**所有內網網段**（RFC1918、loopback、CGNAT、IPv6 ULA、link-local 等），只放行列出的 CIDR

若 SMTP 主機在內網，必須在此列出，例如：

```json
"allowed_internal_hosts": ["10.0.0.0/8", "192.168.1.25/32"]
```

### 命令列參數

```bash
./gophish --config /path/to/config.json   # 指定設定檔，預設 ./config.json
./gophish --mode all                      # all（預設）/ admin / phish
./gophish --disable-mailer                # 關閉內建寄信，供多機部署使用
./gophish --version
```

---

## 資料庫

### SQLite3（預設）

無需額外安裝，啟動時自動建立：

```json
"db_name": "sqlite3",
"db_path": "gophish.db"
```

適合單機、中小規模演練。注意 gophish 將資料庫連線池上限設為 1，高併發下會成為瓶頸。

### MySQL

連線字串格式（**注意不是標準 URL**，主機要用括號包起來）：

```json
"db_name": "mysql",
"db_path": "gophish:密碼@(db.example.com:3306)/gophish?charset=utf8&parseTime=True&loc=Local"
```

建立資料庫與使用者：

```sql
CREATE DATABASE gophish CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'gophish'@'%' IDENTIFIED BY '請換成強密碼';
GRANT ALL PRIVILEGES ON gophish.* TO 'gophish'@'%';
FLUSH PRIVILEGES;
```

Migration 會在首次啟動時自動套用，無需手動執行。

#### 關於 MySQL 8 的 sql_mode

gophish 使用「零值時間」表示「這件事還沒發生」——例如使用者首次登入前的 `last_login`、活動結束前的 `completed_date`、未指定寄送期限時的 `send_by_date`。這些會序列化成 `0000-00-00`，而 **MySQL 8 預設的 `NO_ZERO_DATE` 會拒絕它**。

上游 gophish 在這個情況下會在建立預設管理員帳號時直接啟動失敗：

```
level=fatal msg="Error 1292: Incorrect datetime value: '0000-00-00' for column 'last_login' at row 1"
```

本分支會在你的連線字串**沒有指定 `sql_mode`** 時自動補上一組可用的值（MySQL 8 預設值移除 `NO_ZERO_DATE` 與 `NO_ZERO_IN_DATE`，**保留 `STRICT_TRANS_TABLES`**，其餘不變）。因此直接使用上面的連線字串即可，不需任何額外設定。

若你在連線字串中自行指定了 `sql_mode`，gophish 會完全尊重你的設定、不做任何修改。此時請自行確認它允許零值日期。

#### MySQL over TLS

```json
"db_sslca_path": "/etc/ssl/certs/mysql-ca.pem",
"db_path": "gophish:密碼@(db.example.com:3306)/gophish?charset=utf8&parseTime=True&loc=Local&tls=ssl_ca"
```

---

## 首次啟動

```bash
./gophish
```

啟動日誌會印出隨機產生的管理員臨時密碼：

```
time="..." level=info msg="Please login with the username admin and the password REDACTED-SEE-YOUR-OWN-LOG"
time="..." level=info msg="Starting admin server at https://127.0.0.1:3333"
time="..." level=info msg="Starting phishing server at http://0.0.0.0:80"
```

以 `admin` 與該密碼登入 `https://127.0.0.1:3333`，系統會**強制要求修改密碼**。自簽憑證會造成瀏覽器警告，屬正常現象。

### 用環境變數指定初始憑證

適合自動化部署：

```bash
export GOPHISH_INITIAL_ADMIN_PASSWORD='請換成強密碼'
export GOPHISH_INITIAL_ADMIN_API_TOKEN='自訂的 API token'
./gophish
```

兩者都只在資料庫為空時生效。即使指定了密碼，首次登入仍會要求變更。

### 密碼政策

最短 8 字元，以 bcrypt（預設 cost）儲存，不可重複使用前一組密碼。登入端點有速率限制：每個來源 IP 每分鐘 5 次 POST，超過回傳 429。

---

## 正式部署

### 建議作法：`deploy/install.sh`

Ubuntu 上直接用內附的安裝腳本。它會建立系統使用者、安裝到 `/opt/gophish`、產生設定檔、安裝已加固的 systemd unit 並啟動服務：

```bash
curl -fsSLO https://raw.githubusercontent.com/tonylin2026-debug/phish_wu/master/deploy/install.sh
chmod +x install.sh
sudo ./install.sh --release latest \
     --admin-domain admin.example.com \
     --contact-address security@example.com
```

也可以指定本機的 zip：`--package ./gophish-v0.12.1-linux-64bit.zip`。

**這個腳本會在每次推送時於真實的 Ubuntu runner 上被完整執行驗證**（`Ubuntu deployment` workflow）：服務以非 root 身分啟動、兩個監聽埠都只綁 loopback、管理介面回應登入頁、釣魚伺服器的四個端點正常、設定檔逐欄檢查、檔案權限檢查，並實際跑一次升級確認資料與設定都沒被覆蓋。

它採用的拓樸是：

```
nginx :443 ──► 127.0.0.1:3333   管理介面（TLS、自簽、僅 loopback）
      :443 ──► 127.0.0.1:8080   釣魚伺服器（HTTP、僅 loopback）
```

gophish 只綁非特權的 loopback 埠，所以**不需要 root，也不需要 `setcap`**——由 nginx 負責 80/443。

#### 升級

重跑同一個指令即可。`gophish.db`、`config.json`（含你的手動編輯）與自動產生的管理介面憑證都會保留：

```bash
sudo ./install.sh --release latest
```

#### 腳本參數

| 參數 | 說明 |
|---|---|
| `--release <tag\|latest>` | 從本專案的 GitHub Release 下載 |
| `--package <path\|url>` | 改用指定的 zip |
| `--admin-domain <host>` | 管理介面網域，會寫入 `trusted_origins` |
| `--contact-address <mail>` | 演練聯絡信箱 |
| `--install-dir <path>` | 預設 `/opt/gophish` |
| `--user <name>` | 預設 `gophish` |
| `--admin-listen <ip:port>` | 預設 `127.0.0.1:3333` |
| `--phish-listen <ip:port>` | 預設 `127.0.0.1:8080` |
| `--no-start` | 只安裝不啟動 |

---

以下是手動部署的細節，若你用上面的腳本就不需要自己做。

### 手動：目錄配置

```bash
sudo useradd -r -m -d /opt/gophish -s /usr/sbin/nologin gophish
sudo -u gophish mkdir -p /opt/gophish
# 將執行檔與上述「執行時需要的檔案」全部放入 /opt/gophish
sudo chown -R gophish:gophish /opt/gophish
```

### 手動：若要讓 gophish 直接綁 80／443

只有在不使用反向代理時才需要。不要用 root 執行，改用 capability：

```bash
sudo setcap 'cap_net_bind_service=+ep' /opt/gophish/gophish
```

> 每次替換執行檔後都要重新執行，capability 會隨檔案被覆寫而消失。這也是建議改用 nginx 的原因之一。

### systemd

`/etc/systemd/system/gophish.service`：

```ini
[Unit]
Description=Gophish phishing simulation server
After=network.target mysql.service

[Service]
Type=simple
User=gophish
Group=gophish
WorkingDirectory=/opt/gophish
ExecStart=/opt/gophish/gophish
Restart=on-failure
RestartSec=5

# 基本加固
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/gophish
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gophish
sudo journalctl -u gophish -f
```

> `WorkingDirectory` 必須正確設定。gophish 以相對路徑讀取 `VERSION`、`templates/`、`static/`、`db/`，工作目錄錯了會啟動失敗。

### 反向代理（nginx）

管理介面維持只聽 localhost，由 nginx 處理對外 TLS：

```nginx
server {
    listen 443 ssl http2;
    server_name admin.example.com;

    ssl_certificate     /etc/letsencrypt/live/admin.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/admin.example.com/privkey.pem;

    location / {
        proxy_pass         https://127.0.0.1:3333;
        proxy_ssl_verify   off;          # 後端為自簽憑證
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
    }
}
```

**使用反向代理時務必設定 `trusted_origins`**，否則 CSRF 檢查會擋下登入：

```json
"trusted_origins": ["admin.example.com"]
```

gophish 會讀取 `X-Forwarded-For` / `X-Real-IP`，因此記錄到的受測者來源 IP 是真實位址而非代理位址。

### 釣魚伺服器的對外設定

釣魚伺服器要讓受測者連得到。若使用 nginx 終結 TLS：

```json
"phish_server": { "listen_url": "127.0.0.1:8080", "use_tls": false }
```

```nginx
server {
    listen 443 ssl http2;
    server_name phish.example.com;
    ssl_certificate     /etc/letsencrypt/live/phish.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/phish.example.com/privkey.pem;

    location / {
        proxy_pass       http://127.0.0.1:8080;
        proxy_set_header Host            $host;
        proxy_set_header X-Real-IP       $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

### 防火牆

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

管理介面的 3333 **不要對外開放**。

### Docker

專案內含 `Dockerfile` 與 `docker/run.sh`，後者支援以環境變數覆寫設定：

```bash
docker build -t phish_wu .
docker run -d --name phish_wu \
  -p 3333:3333 -p 80:80 \
  -e ADMIN_LISTEN_URL=0.0.0.0:3333 \
  -e PHISH_LISTEN_URL=0.0.0.0:80 \
  -e CONTACT_ADDRESS=security@example.com \
  -e DB_NAME=mysql \
  -e DB_FILE_PATH='gophish:密碼@(mysql:3306)/gophish?charset=utf8&parseTime=True&loc=Local' \
  phish_wu
```

可用的環境變數：`ADMIN_LISTEN_URL`、`ADMIN_USE_TLS`、`ADMIN_CERT_PATH`、`ADMIN_KEY_PATH`、`ADMIN_TRUSTED_ORIGINS`、`PHISH_LISTEN_URL`、`PHISH_USE_TLS`、`PHISH_CERT_PATH`、`PHISH_KEY_PATH`、`CONTACT_ADDRESS`、`DB_NAME`、`DB_FILE_PATH`。

> 現行 `Dockerfile` 使用 `golang:1.15.2` 與未鎖版本的 `node:latest`。若要正式使用，建議先將 Go 版本更新至與 CI 一致（1.22 或 1.23）並鎖定 node 版本。

### Ansible

多台主機時使用。這個 role 很薄——它把 `deploy/install.sh` 複製到目標主機並執行，所以安裝邏輯只有一份，而且是被 CI 測過的那一份。另外負責防火牆規則與 nginx 設定。

```bash
ansible-galaxy collection install community.general

cd ansible-playbook
# 編輯 hosts 與 roles/gophish/defaults/main.yml
ansible-playbook site.yml -i hosts -u ubuntu --become --private-key=~/.ssh/id_ed25519
```

詳見 [ansible-playbook/README.md](ansible-playbook/README.md)。

### 多機部署

管理介面與釣魚伺服器可分離：

```bash
# A 機（內網）：管理介面 + 寄信
./gophish --mode admin

# B 機（對外）：只跑釣魚伺服器
./gophish --mode phish
```

兩台需指向同一個資料庫（此時必須用 MySQL）。若寄信由 A 機負責，B 機應加上 `--disable-mailer`。

---

## 三動作追蹤設定

這是本分支的核心功能。要讓**同一封信**同時具備三個動作的追蹤能力，需要在樣板中放入三個不同的元素。

### 1. 開啟郵件

在 Email Template 的 **HTML** 分頁勾選「Add Tracking Image」，系統會自動在 `</body>` 前插入：

```html
{{.Tracker}}
```

展開後是一個 1×1 的隱藏圖片，指向 `/track`。

> **純文字信件無法追蹤開信**。沒有 HTML 內容就無法嵌入像素。

### 2. 點擊連結

在信件內容中使用：

```html
<a href="{{.URL}}">請點此驗證您的帳號</a>
```

受測者點擊後會抵達落地頁，記錄為 `Clicked Link`。若落地頁含表單且啟用了「Capture Submitted Data」，提交後額外記錄 `Submitted Data`。

### 3. 開啟附件

在附件**內部**放入指向 `/track/attachment` 的遠端資源。可用兩個變數：

| 變數 | 展開結果 |
|---|---|
| `{{.AttachmentTrackingURL}}` | 純網址 |
| `{{.Tracker}}` | 完整的隱藏 `<img>` 標籤 |

> 在附件內，`{{.Tracker}}` 與 `{{.TrackingURL}}` 會**自動改為指向附件端點**，因此沿用舊樣板也能正確分類。但建議明確使用 `{{.AttachmentTrackingURL}}`，意圖較清楚。

#### 支援套用樣板的附件格式

| 格式 | 處理方式 |
|---|---|
| `.docx` `.docm` `.pptx` `.xlsx` `.xlsm` | 解開 zip，對內部 `.xml` 與 `.rels` 套用樣板後重新打包 |
| `.txt` `.html` `.ics` | 直接套用樣板 |
| 其他 | 原樣附加，不做處理 |

#### Word 文件的作法

在 Word 中插入一個 `INCLUDEPICTURE` 功能變數（<kbd>Ctrl</kbd>+<kbd>F9</kbd> 插入大括號）：

```
{ INCLUDEPICTURE "{{.AttachmentTrackingURL}}" \d }
```

> Word 有時會把 `{{.Foo}}` 轉義成 `%7b%7b.foo%7d%7d`。gophish 會自動還原這種轉義，所以即使 Word 做了轉換仍可正常運作。

#### HTML 附件的作法

```html
<img src="{{.AttachmentTrackingURL}}" width="1" height="1" alt="" style="display:none">
```

### 三者的互動關係

| 情境 | `email_opened` | `attachment_opened` | `clicked_link` | `Status` |
|---|:--:|:--:|:--:|---|
| 只開信 | ✅ | ✖ | ✖ | Email Opened |
| 只開附件（圖片被擋） | ✖ | ✅ | ✖ | Email Sent |
| 先點連結再開附件 | ✖ | ✅ | ✅ | Clicked Link |
| 三個都做 | ✅ | ✅ | ✅ | Clicked Link |
| 提交表單 | — | — | ✅（自動） | Submitted Data |

重點：

- 開啟附件**不會**被誤判為開啟郵件
- 開啟附件**不會**改變 `Status`，所以不會覆蓋掉已記錄的「點擊」或「提交」
- 提交表單會自動補上 `clicked_link`（提交必然經過點擊）
- 動作的**抵達順序不影響**最終記錄

### 結果頁面的呈現

活動結果頁的每一列會顯示三個獨立圖示（信封／迴紋針／滑鼠游標），一眼就能看出該收件者做了哪些動作。圖表區下方另有一行：

```
Email Opened (measured by tracking pixel): 12 (24%) · Reached (opened, attachment opened, clicked or submitted): 31 (62%)
```

### 匯出

- **Export CSV → Results**：每位收件者一列，含四個動作欄位
- **Export CSV → Raw Events**：完整事件時間軸，含每次開啟的時間與裝置資訊

---

## 樣板變數

可用於信件主旨、內文、附件與落地頁：

| 變數 | 說明 |
|---|---|
| `{{.RId}}` | 收件者的唯一識別碼（7 字元） |
| `{{.FirstName}}` | 名 |
| `{{.LastName}}` | 姓 |
| `{{.Position}}` | 職稱 |
| `{{.Email}}` | 電子郵件 |
| `{{.From}}` | 寄件者位址 |
| `{{.URL}}` | 落地頁網址（已帶 `rid` 參數） |
| `{{.BaseURL}}` | 去除路徑與參數的基底網址，適合引用靜態檔案 |
| `{{.TrackingURL}}` | 開信追蹤網址（在附件內會自動改為附件端點） |
| `{{.Tracker}}` | 完整的隱藏 `<img>` 標籤（在附件內會自動改為附件端點） |
| `{{.AttachmentTrackingURL}}` | 附件開啟追蹤網址 |

在 HTML 編輯器中輸入 `{{.` 會跳出自動完成清單。

---

## REST API

所有端點位於 `/api/`，使用 API Key 驗證：

```bash
curl -H "Authorization: Bearer $API_KEY" https://127.0.0.1:3333/api/campaigns/
```

API Key 可在「Account Settings」頁面取得或重設。

### 活動統計的回應欄位

```json
{
  "total": 50,
  "sent": 50,
  "opened": 12,
  "engaged": 31,
  "attachment_opened": 18,
  "clicked": 25,
  "submitted_data": 7,
  "email_reported": 3,
  "error": 0
}
```

| 欄位 | 說明 |
|---|---|
| `opened` | 追蹤像素實測開信數 |
| `engaged` | 有任何互動的人數（不受圖片阻擋影響） |
| `attachment_opened` | 開啟附件人數 |

### 單筆結果的回應欄位

```json
{
  "id": "aBc1234",
  "status": "Clicked Link",
  "email_opened": true,
  "attachment_opened": true,
  "clicked_link": true,
  "submitted_data": false,
  "reported": false,
  "ip": "203.0.113.5",
  "latitude": 25.0,
  "longitude": 121.5,
  "send_date": "...",
  "modified_date": "..."
}
```

### 主要端點

| 方法 | 路徑 |
|---|---|
| GET / POST | `/api/campaigns/` |
| GET | `/api/campaigns/summary` |
| GET / DELETE | `/api/campaigns/:id` |
| GET | `/api/campaigns/:id/results` |
| GET | `/api/campaigns/:id/summary` |
| GET | `/api/campaigns/:id/complete` |
| GET / POST | `/api/groups/`、`/api/templates/`、`/api/pages/`、`/api/smtp/` |
| GET / PUT / DELETE | 上述各資源的 `/:id` |
| POST | `/api/import/group`、`/api/import/email`、`/api/import/site` |
| POST | `/api/util/send_test_email` |
| POST | `/api/reset`（重設自己的 API Key） |
| GET / POST | `/api/users/`、`/api/webhooks/`（需 admin 權限） |

> `/api/*` 豁免 CSRF 檢查，僅依賴 API Key，且回應帶有 `Access-Control-Allow-Origin: *`。請將 API Key 視同密碼保管。

---

## 使用者回報（IMAP）

於「Settings → Reporting Settings」設定一個信箱，gophish 會定期輪詢未讀信件，從內文與附件中比對 `rid` 參數，自動將對應的收件者標記為「已回報」。

| 設定 | 說明 |
|---|---|
| Polling frequency | 最短 30 秒，預設 60 秒 |
| Restrict to domain | 只處理來自指定網域的回報 |
| Delete campaigns emails | 回報處理完成後刪除該信件 |
| Ignore Certificate Errors | **預設關閉，建議維持關閉** |

亦可直接呼叫釣魚伺服器的 `/report?rid=<RID>` 端點（該端點允許跨來源請求，供瀏覽器外掛使用）。

---

## Webhooks

於「Webhooks」頁面（需 admin 權限）設定接收端點。每次活動事件發生時會以 POST 送出 JSON，並附帶簽章標頭：

```
X-Gophish-Signature: sha256=<HMAC-SHA256(secret, body)>
```

接收端應驗證此簽章。事件種類包含 `Email Sent`、`Email Opened`、**`Attachment Opened`**、`Clicked Link`、`Submitted Data`、`Email Reported`。

---

## 安全性注意事項

以下皆為**實際存在於目前程式碼中**的行為，部署前請評估。

### 憑證以明文儲存

- 落地頁攔截到的帳號密碼**未加密**存放於資料庫（UI 內亦有此警告）
- Sending Profile 的 SMTP 密碼明文存放，**且 API 會在回應中回傳**
- IMAP 密碼明文存放，設定頁面載入時會透過 API 傳送到瀏覽器

請確實限制資料庫與管理介面的存取權限，並在演練結束後清理資料。

### 預設值

| 項目 | 預設 | 建議 |
|---|---|---|
| Sending Profile 的「Ignore Certificate Errors」 | **已勾選** | 取消勾選，除非確有需要 |
| `phish_server.listen_url` | `0.0.0.0:80` | 若有反向代理，改為 `127.0.0.1:8080` |
| `admin_server.csrf_key` | 留空（每次啟動隨機） | 正式環境請固定，否則重啟即登出所有人 |

Session 簽章金鑰在每次啟動時隨機產生，**重啟後所有登入狀態一律失效**；多機部署時 session 也無法共用。

### 其他

- 「Import Site」功能複製外部網站時**不驗證 TLS 憑證**
- 管理介面每一頁的 inline script 都嵌有當前使用者的 API Key，管理介面的任何 XSS 都等同洩漏完整帳號權限
- `/impersonate` 允許 admin 不輸入密碼切換為任意使用者身分（需 `modify_system` 權限）
- 相依套件多為 2019–2020 年版本

---

## 開發與測試

```bash
gofmt -l .                                  # CI 會擋格式問題，必須無輸出
go vet ./...
go build -v .
go test ./... -count=1
go test -race -count=1 ./models/... ./controllers/...
```

### 持續整合

| Workflow | 內容 |
|---|---|
| `CI` | Go 1.21 / 1.22 / 1.23 的建置、`gofmt` 檢查、完整測試、race detector |
| `MySQL migrations` | 於真實 `mysql:8.0` 容器套用全部 migration、驗證 schema、透過 API 建立活動、驗證歷史資料回填 |
| `Ubuntu deployment` | 在真實 Ubuntu runner 上執行 `deploy/install.sh`：shellcheck、systemd 服務啟動、非 root 身分、loopback 綁定、各端點回應、設定與權限檢查、就地升級、`nginx -t` 驗證範本 |
| `Build Gophish Release` | 建立 GitHub Release 時觸發，產出 Windows / Linux / macOS 的 zip |

### 只跑三動作追蹤的測試

```bash
go test ./models/ ./controllers/ -run 'Action|ThreeActions|Concurrent|Attachment' -v -count=1
```

涵蓋三個動作的六種排列順序、併發請求的競態回歸、只開附件不開信、重複開啟的冪等性，以及統計數字的正確性。

---

## 授權

MIT License。上游 Gophish 著作權歸 Jordan Wright（2013–2020）所有，詳見 [LICENSE](LICENSE)。

上游專案文件：<https://docs.getgophish.com/>
