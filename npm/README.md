# __PKG__

`sepay` là công cụ dòng lệnh của **SePay**, công ty hạ tầng Open Banking tại Việt Nam. Đăng nhập, xem
giao dịch, tạo webhook và nhận webhook ngay trên `localhost`, tạo đơn hàng thanh toán hosted checkout,
và giả lập toàn bộ các bước đó trên sandbox.

## Cài đặt

```sh
npm install --save-dev __PKG__@__VERSION__
npx __PKG__ version
```

**Luôn ghim phiên bản.** `__PKG__@latest` và `npx __PKG__` không kèm phiên bản sẽ lấy bản mới nhất ở
thời điểm chạy, ngược với mục đích của lockfile. Bản thử nghiệm có dist-tag riêng (`alpha`, `beta`,
`next` cho release candidate) và không bao giờ trở thành `latest`.

Trong CI, cài theo lockfile và tắt install script:

```sh
npm ci --ignore-scripts
npx __PKG__ version
```

Gói này không có install script nào, nên `--ignore-scripts` không làm mất gì ở đây, và flag đó vẫn
có tác dụng với mọi dependency khác trong project.

## Cách hoạt động

`__PKG__` không chứa binary. Gói khai báo sáu optional dependency, mỗi nền tảng một gói, và npm chỉ
cài gói đúng với máy của bạn:

| Nền tảng | Gói |
|---|---|
| macOS Intel | `__SCOPE__/cli-darwin-x64` |
| macOS Apple silicon | `__SCOPE__/cli-darwin-arm64` |
| Linux x86-64 | `__SCOPE__/cli-linux-x64` |
| Linux arm64 | `__SCOPE__/cli-linux-arm64` |
| Windows x86-64 | `__SCOPE__/cli-win32-x64` |
| Windows arm64 | `__SCOPE__/cli-win32-arm64` |

Trước khi chạy binary, `__PKG__` so SHA-256 của binary với bảng có sẵn trong gói này, và không chạy
nếu không khớp.

`__SCOPE__` là npm org của Công ty CP SePay (SePay JSC), nơi phát hành cả bảy gói. Mã nguồn nằm trong
GitHub org của công ty, tại <https://github.com/sepayvn/sepay-cli>. Tên org trên npm (`__SCOPE__`) và
trên GitHub (`sepayvn`) khác nhau nhưng cùng một chủ sở hữu.

## Kiểm tra nguồn gốc của gói

Kiểm bằng hai lệnh sau, đừng dựa vào badge:

```sh
npm audit signatures
npm view __PKG__ repository.url
```

Lệnh thứ hai phải in ra `git+https://github.com/sepayvn/sepay-cli.git`. Chỉ thấy gói "có
provenance" thì chưa đủ: provenance chỉ cho biết gói được build từ *một* repo nào đó. Ai chiếm được
tên gói cũng phát hành kèm provenance từ repo của họ được, và lệnh thứ nhất vẫn báo qua. **Phải nhìn
tên repo mới biết gói có phải của SePay hay không.**

## Giới hạn của kênh npm

1. **Không có shell completion.** Đăng ký completion cần install script, mà gói này cố ý không có.
   Chạy `npx __PKG__ completion --help` để tự cài.
2. **Không kiểm tra cosign lúc cài.** npm không có cơ chế tương đương. Ở kênh này chữ ký cosign của
   `checksums.txt` được kiểm **một lần, trong pipeline release**, và bạn nhận được bảng checksum
   lấy từ file đã ký đó.
3. **`npm install --omit=optional` (hoặc `--no-optional`) làm gói không chạy được.** Binary nằm trong
   optional dependency, bỏ qua chúng là bỏ qua binary. Thông báo lỗi của gói nói rõ điều này.

## Tải bản khác

Archive cho từng nền tảng và `checksums.txt` có chữ ký được đính kèm ở mỗi GitHub Release:
<https://github.com/sepayvn/sepay-cli/releases>

Tài liệu: <https://developer.sepay.vn/vi/agents/cli/cai-dat>

Apache-2.0 © Công ty CP SePay
