# Chính sách bảo mật

## Báo cáo lỗ hổng

Vui lòng báo cáo vấn đề bảo mật một cách riêng tư qua **support@sepay.vn**; không mở issue công khai
cho bất cứ điều gì nhạy cảm về bảo mật. Gửi kèm các bước tái hiện và phiên bản bị ảnh hưởng
(`sepay version`). Chúng tôi cố gắng xác nhận đã nhận trong vòng vài ngày làm việc.

## Cách xử lý một phiên bản có lỗ hổng

1. Xác định dải phiên bản bị ảnh hưởng (`git tag --contains <commit that introduced it>`).
2. Vấn đề nghiêm trọng: các bản cài mới được chuyển ngay sang một phiên bản an toàn (dist-tag
   `latest`) trong lúc chuẩn bị bản sửa.
3. Bản sửa được phát hành dưới dạng bản patch theo quy trình release thông thường.
4. Mọi phiên bản bị ảnh hưởng của cả 7 gói npm được đánh dấu bằng `npm deprecate`, trỏ tới phiên bản
   đã sửa, và các GitHub Release bị ảnh hưởng có thêm cảnh báo ở đầu phần ghi chú.
5. Chi tiết được công bố sau khi người dùng đã có thời gian nâng cấp.

Trước 1.0, chỉ bản release mới nhất được nhận bản sửa.

## Chính sách không chứa secret

`sepay` là mã nguồn mở và **không chứa secret nào**. CLI không bao giờ được nhúng API key, token,
khóa ký, endpoint nội bộ hay thông tin xác thực của host.

- CLI chỉ xác thực bằng **OAuth2 token** mà người dùng lấy qua `sepay login`; CLI không bao giờ
  mang theo API key tĩnh của SePay.
- Token được lưu trong **keychain của hệ điều hành**, hoặc trong một file `0600` khi người dùng chọn
  `--insecure-storage`, hoặc khi `sepay login` không tìm thấy keychain dùng được (CLI báo điều này
  bằng một cảnh báo và ghi lựa chọn đó vào profile). Keychain lỗi sau khi đã đăng nhập là một lỗi,
  không bao giờ âm thầm chuyển sang file.
  Token **không bao giờ được in ra** (`login`, `auth status`, `--verbose` và các thông báo lỗi đều
  che token). Chỉ `sepay auth token` in token ra, và chỉ khi được gọi rõ ràng.
- Phiên đăng nhập chỉ đến từ `sepay login`; không có biến môi trường nào đưa token vào.
- CLI chỉ gọi SePay API **công khai**, không gọi endpoint riêng của CLI hay endpoint nội bộ. Host
  duy nhất khác là `registry.npmjs.org`, được hỏi tối đa một lần mỗi ngày để lấy phiên bản mới nhất;
  host này không nhận thông tin xác thực nào (`SEPAY_NO_UPDATE_NOTIFIER=1` tắt việc này).

Nếu bạn phát hiện secret bị viết cứng trong repository hoặc trong một binary đã phát hành, hãy coi đó
là lỗ hổng và báo cáo như trên.

## Chuỗi cung ứng

- Mọi commit đều được quét bằng gitleaks trong CI; thông tin xác thực giả trong test được đánh dấu
  `// gitleaks:allow` trên chính dòng của nó.
- Binary phát hành chỉ được build bởi GitHub Actions (`.github/workflows/release.yml`), mọi action
  đều được ghim bằng commit SHA đầy đủ.
- `checksums.txt` được ký keyless bằng cosign (GitHub OIDC); job npm từ chối đóng gói archive có
  checksum không nằm trong file đã ký.
- Các gói npm được phát hành bởi chính workflow đó qua npm trusted publishing; không có npm token nào
  được lưu trong repository.

## Các phiên bản được hỗ trợ

Trong giai đoạn phát triển trước 1.0, chỉ bản release mới nhất được hỗ trợ.
