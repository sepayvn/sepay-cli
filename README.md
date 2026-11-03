# sepay

Công cụ dòng lệnh của **SePay**. Đăng nhập, xem giao dịch, tạo webhook và nhận webhook ngay trên
`localhost`, tạo đơn hàng thanh toán hosted checkout, và giả lập toàn bộ các bước đó trên sandbox.

```
sepay <command> <subcommand> [flags]
```

## Cài đặt

```bash
npm install --save-dev sepay-cli@<version>   # ghim phiên bản
npx sepay-cli version
npx sepay-cli@next version                    # bản release candidate mới nhất
```

Gói cài đặt cho từng nền tảng và file `checksums.txt` có chữ ký cosign được đính kèm ở mỗi
[GitHub Release](https://github.com/sepayvn/sepay-cli/releases).

### Cập nhật

```bash
npm install -g sepay-cli@latest      # cài toàn cục
npm install -D sepay-cli@latest      # ghim trong project (hoặc để Dependabot / Renovate mở PR)
```

Mỗi ngày một lần, khi bạn chạy lệnh trên terminal, CLI kiểm tra xem đã có bản mới chưa và bản
đang dùng có bị ngừng hỗ trợ (bị thu hồi hoặc có lỗ hổng) không, rồi in một dòng thông báo ngắn ra
stderr sau khi lệnh chạy xong. CLI không bao giờ tự cài gì, và không thông báo khi chạy trong CI,
khi dùng `--json`, hoặc khi đặt `SEPAY_NO_UPDATE_NOTIFIER=1`.

## Bắt đầu

```bash
sepay login          # mở trình duyệt để xác nhận một mã dùng một lần
sepay auth status    # tài khoản và công ty bạn đang dùng
```

Token được lưu trong keychain của hệ điều hành. Trên máy không có keychain dùng được (server, máy
truy cập qua SSH), CLI tự lưu token vào một file chỉ tài khoản của bạn đọc được và in cảnh báo màu
vàng; bạn không cần làm gì thêm. Muốn lưu vào file ngay từ đầu: `sepay login --insecure-storage`.
Máy không có trình duyệt: `sepay login --with-token`.

Mọi lệnh mặc định chạy trên **sandbox**. Thêm `--env production` để làm việc với dữ liệu thật.

## Nhận webhook đầu tiên trên localhost

Không cần URL công khai, không cần tunnel.

```bash
# Handler của bạn đang chạy ở localhost:3000

sepay sandbox init --with-va --with-webhook-listen        # tạo tài khoản sandbox + webhook
sepay webhook listen --forward localhost:3000 --print     # chuyển sự kiện về handler

# Mở terminal thứ hai: tạo một giao dịch
sepay bank-account list                                   # ghi lại xid
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000 --code ABC123
```

Mỗi sự kiện mang đúng body và các header chữ ký mà SePay gửi, nên handler chạy đúng luồng thật,
kể cả bước tự kiểm tra chữ ký. Kiểm tra handler phản hồi đúng: `sepay webhook test --forward localhost:3000 --amount 50000`.

## Các lệnh

| Lệnh | Chức năng |
|---|---|
| `transaction` | Xem danh sách và chi tiết giao dịch |
| `bank-account` | Quản lý tài khoản ngân hàng |
| `webhook` | Tạo, sửa, xem log, `listen`, `test`, `replay` |
| `va` | Quản lý tài khoản ảo (VA) |
| `paycode` | Tiền tố mã thanh toán |
| `pg` | Đơn hàng và merchant của Cổng thanh toán (Payment Gateway) |
| `bankhub` | Công ty, tài khoản và link token của Bank Hub |
| `einvoice` | Hóa đơn điện tử |
| `apikey` | API key |
| `quota` | Hạn mức gói và mức sử dụng |
| `sandbox` | `init`, `seed`, `simulate`, `reset` (chỉ trên sandbox) |
| `api` | Gọi bất kỳ endpoint nào của SePay API bằng token đã lưu |
| `auth` | `status`, `switch`, `refresh`, `sessions`, `revoke`, `token` |
| `config` | Cấu hình CLI |
| `docs` | Tài liệu tra cứu ngay trong terminal |

`sepay --help` liệt kê toàn bộ. `sepay --map=paths` in mọi lệnh; `--map=json` kèm thêm metadata
cho máy đọc.

## Flag dùng chung

| Flag | Ý nghĩa |
|---|---|
| `--env sandbox\|production` | Môi trường, mặc định `sandbox` |
| `--json`, `-o json\|yaml` | Output cho máy đọc, không màu; mọi lệnh đọc dữ liệu đều hỗ trợ |
| `-p, --profile` | Tài khoản cần dùng, khi đang đăng nhập nhiều tài khoản |
| `-y, --yes` | Bỏ bước xác nhận ở lệnh ghi (script, CI); không bỏ qua được OTP |
| `--no-input` | Không bao giờ hỏi; thiếu giá trị thì báo lỗi |
| `--idempotency-key` | Chạy lại một lệnh ghi bị gián đoạn mà không thực hiện hai lần |
| `-v, --verbose` | Log gỡ lỗi; token và OTP luôn được che |

Biến môi trường: `SEPAY_PROFILE`, `SEPAY_CONFIG_DIR`, `SEPAY_NO_UPDATE_NOTIFIER`, `NO_COLOR`. Chi tiết: `sepay docs environment`.

## Mã thoát

| Mã | Ý nghĩa |
|---|---|
| 0 | Thành công |
| 1 | Lỗi phía server, lỗi mạng, hoặc dữ liệu bị từ chối; đọc `error_code` trước khi chạy lại |
| 2 | Sai flag, sai tham số, lệnh không tồn tại |
| 4 | Chưa đăng nhập: `sepay login` |
| 5 | Token thiếu quyền (scope) cần thiết: `sepay auth refresh --scopes <scope>` |
| 6 | Handler trên máy bạn không nhận sự kiện (`webhook test`) |
| 7 | Server yêu cầu OTP; hãy thực hiện thao tác này trên web |
| 8 | Tài khoản hoặc công ty của bạn không được phép làm việc này |

Lỗi in ra `stderr`, kết quả in ra `stdout`. Khi dùng `--json`, lỗi cũng ở dạng JSON, kèm
`error_code` và `exit_code`. Danh sách đầy đủ: `sepay docs exit-codes`.

## Tự hoàn thành lệnh (shell completion)

Nếu cài từ gói release hoặc npm, chạy một lần:

```bash
mkdir -p ~/.local/share/bash-completion/completions && sepay completion bash > ~/.local/share/bash-completion/completions/sepay   # cần bash-completion
mkdir -p ~/.zfunc && sepay completion zsh > ~/.zfunc/_sepay   # ~/.zshrc: fpath+=~/.zfunc trước compinit
sepay completion fish > ~/.config/fish/completions/sepay.fish
sepay completion powershell | Out-String | Invoke-Expression   # thêm vào $PROFILE
```

## Tài liệu

| Chủ đề | Lệnh |
|---|---|
| SePay là gì, chọn cách thanh toán nào | `sepay docs overview` |
| Thứ tự lệnh cho các tác vụ thường gặp | `sepay docs flows` |
| Body webhook và các header chữ ký | `sepay docs webhook-payload` |
| IPN đơn hàng của Cổng thanh toán | `sepay docs ipn-payload` |
| Gọi SePay API trực tiếp từ ứng dụng của bạn | `sepay docs api` |

Tài liệu đầy đủ: [developer.sepay.vn/vi/tien-ich-khac/sepay-cli](https://developer.sepay.vn/vi/tien-ich-khac/sepay-cli).

## Đóng góp

[CONTRIBUTING.md](CONTRIBUTING.md). Báo cáo lỗ hổng bảo mật: [SECURITY.md](SECURITY.md).

## Giấy phép

[Apache-2.0](LICENSE) © SePay JSC.
