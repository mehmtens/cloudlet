# Production release checklist

Cloudlet deployment-ready durumdadır. Canlı altyapı doğrulanmadığı için production deployment tamamlandı olarak işaretlenmemiştir.

## Hazırlık

1. `.env.production.example` dosyasını `.env.production` olarak kopyalayın ve yalnızca deployment ortamında gerçek değerlerle doldurun.
2. DNS kaydını sunucuya yönlendirin ve güvenlik duvarında TCP 80/443 portlarını açın. Caddy sertifikayı otomatik alır ve HTTPS'e yönlendirir.
3. Yönetilen PostgreSQL bağlantısında TLS'i etkin bırakın. Uygulama başlangıçta gömülü, idempotent migration'ları çalıştırır ve başarısız olursa açılmaz.
4. S3/R2 bucket'ında public access'i kapatın, uygulama kimliğine yalnızca gereken bucket yetkilerini verin ve CORS origin olarak `S3_CORS_ORIGIN` değerini tanımlayın. `S3_PUBLIC_ENDPOINT`, tarayıcının erişebildiği presigned URL endpoint'i olmalıdır.
5. Doğrulanmış gönderici domainine sahip gerçek SMTP hesabını STARTTLS ile yapılandırın.
6. Başlatın:

```bash
docker compose --env-file .env.production -f compose.production.yaml up -d --build
```

Oracle Always Free gibi tek sunuculu kurulumda PostgreSQL'i yalnızca Docker ağına açık şekilde aynı makinede çalıştırın:

```bash
docker compose --env-file .env.production -f compose.production.yaml -f compose.oracle.yaml up -d --build
```

Bu kurulumda `DATABASE_URL=postgres://cloudlet:POSTGRES_PASSWORD@postgres:5432/cloudlet?sslmode=disable` kullanılır. PostgreSQL host portuna açılmaz; `POSTGRES_PASSWORD` URL-safe ve rastgele olmalıdır.

API host portuna açılmaz. Caddy tek public giriş noktasıdır. API readiness kontrolü PostgreSQL bağlantısını doğrular; web healthcheck'i Caddy yönetim endpoint'ini container içinden kontrol eder.

## Canlı doğrulama

- [ ] Üretim PostgreSQL ve S3/R2 bucket oluşturuldu; bucket private.
- [ ] Üretim SMTP hesabı, doğrulanmış domain ve `SMTP_REQUIRE_TLS=true` ayarlandı.
- [ ] `.env.production.example` değerleri gerçek secret’larla dolduruldu; dosya repoya alınmadı.
- [ ] `PUBLIC_BASE_URL` ve `S3_CORS_ORIGIN` HTTPS domain’e ayarlandı.
- [ ] `COOKIE_SECURE=true`, güçlü `JWT_SECRET` ve gerekirse key rotation secret’ı tanımlandı.
- [ ] HTTPS reverse proxy ve DNS yapılandırıldı.
- [ ] Register → gerçek e-posta doğrulama → login → upload → download smoke testi yapıldı.
- [ ] 2FA kurulumu ve TOTP’li login doğrulandı.
- [ ] Backup/restore ve log/metric alarmı test edildi.

Durum ve log kontrolü:

```bash
docker compose --env-file .env.production -f compose.production.yaml ps
docker compose --env-file .env.production -f compose.production.yaml logs --tail=100 api web
curl --fail https://YOUR_DOMAIN/api/health/ready
```

Yerel Docker smoke testi:

```bash
docker compose --profile dev --env-file .env.docker.example up -d --build
```
