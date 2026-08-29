# Production release checklist

Cloudlet’in uygulama özellikleri tamamlandı. Canlıya çıkmadan önce ortam doğrulaması:

- [ ] Üretim PostgreSQL ve S3/R2 bucket oluşturuldu; bucket private.
- [ ] Üretim SMTP hesabı, doğrulanmış domain ve `SMTP_REQUIRE_TLS=true` ayarlandı.
- [ ] `.env.production.example` değerleri gerçek secret’larla dolduruldu; dosya repoya alınmadı.
- [ ] `PUBLIC_BASE_URL` ve `S3_CORS_ORIGIN` HTTPS domain’e ayarlandı.
- [ ] `COOKIE_SECURE=true`, güçlü `JWT_SECRET` ve gerekirse key rotation secret’ı tanımlandı.
- [ ] HTTPS reverse proxy ve DNS yapılandırıldı.
- [ ] Register → gerçek e-posta doğrulama → login → upload → download smoke testi yapıldı.
- [ ] 2FA kurulumu ve TOTP’li login doğrulandı.
- [ ] Backup/restore ve log/metric alarmı test edildi.

Yerel doğrulama komutu:

```bash
docker compose --profile dev --env-file .env.docker.example up -d --build
```
