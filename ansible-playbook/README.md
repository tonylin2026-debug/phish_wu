# Ansible 部署

在 Ubuntu 主機上部署 phish_wu。架構為 SQLite + 外部 SMTP，nginx 負責 80/443，gophish 只聽 loopback。

這個 role 刻意做得很薄：**所有安裝邏輯都在 `deploy/install.sh`**，由 `Ubuntu deployment` workflow 在每次推送時實際執行驗證。在這裡用 Ansible 重寫一遍只會得到兩份會逐漸走樣、而且只有一份被測過的實作。

若你只有一台主機，直接用 `deploy/install.sh` 更簡單，不需要 Ansible。

---

## 需求

控制端：

```bash
ansible-galaxy collection install community.general
```

目標主機：Ubuntu 22.04 或 24.04 LTS，可用 SSH 登入且具備 sudo 權限。

---

## 設定

**1. `hosts`** —— 填入目標主機位址：

```ini
[gophish]
203.0.113.10
```

**2. `roles/gophish/vars/main.yml`** —— 至少要改這幾項：

| 變數 | 說明 |
|---|---|
| `admin_domain` | 管理介面的對外網域。**同時會寫入 `trusted_origins`**，沒設對的話透過反向代理登入會被 CSRF 擋掉 |
| `phish_domain` | 釣魚伺服器的對外網域 |
| `contact_address` | 演練聯絡信箱 |
| `admin_allowed_sources` | **強烈建議設定**。限制能連到管理介面的來源 IP；留空等於把管理介面開放給整個網際網路 |
| `ssh_allowed_from` | SSH 來源限制，預設 `any` |
| `phish_wu_release` | `latest` 或指定的 release tag |

**3. TLS 憑證** —— 這個 role **不會**執行 certbot。請先取得憑證：

```bash
sudo certbot certonly --standalone -d phish.example.com -d admin.example.com
```

或是把 `admin_cert_path` / `phish_cert_path` 等變數指向你既有的憑證。

---

## 執行

```bash
cd ansible-playbook

# 以 SSH 金鑰登入
ansible-playbook site.yml -i hosts -u ubuntu --become --private-key=~/.ssh/id_ed25519

# 以密碼登入
ansible-playbook site.yml -i hosts -u ubuntu --become --ask-pass --ask-become-pass
```

首次執行後，playbook 會從 journal 讀出初始管理員密碼並印出來。登入後系統會強制要求變更。

---

## 升級

重跑同一個 playbook 即可。`install.sh` 的升級是就地進行的：

- `gophish.db`、`config.json`（含你的手動編輯）、自動產生的管理介面憑證**都會保留**
- 服務會在升級期間停止，完成後自動啟動

```bash
ansible-playbook site.yml -i hosts -u ubuntu --become --private-key=~/.ssh/id_ed25519
```

---

## 這個 role 不做的事

| 項目 | 原因 |
|---|---|
| 安裝 Postfix 或任何 MTA | 架構採用外部 SMTP，在 Web UI 的 Sending Profiles 設定 |
| 安裝 MySQL | 採用 SQLite，不需要資料庫伺服器 |
| 執行 certbot | 憑證的取得方式因環境差異大，請自行處理 |
| 產生自簽憑證 | gophish 會自己為管理介面產生；nginx 的憑證請用 certbot |
| 開放 3333 / 8080 | gophish 只綁 loopback，這兩個埠不應對外 |

---

## 與舊版的差異

先前版本的 playbook 有兩個會導致失敗或錯誤結果的問題，已一併修正：

1. **下載的是上游 `gophish/gophish` 的 release**，而不是這個分支。跑完會得到原版 gophish，完全沒有三動作獨立追蹤功能。
2. **使用 `openssl_privatekey` / `openssl_csr` / `openssl_certificate` 等裸模組名**。這些是 collection 化之前的寫法，其中 `openssl_certificate` 在 `community.crypto` 2.0 已被移除，在現代 Ansible 上會直接失敗。

現在不再需要 `community.crypto`，只需要 `community.general`（用於 ufw）。
