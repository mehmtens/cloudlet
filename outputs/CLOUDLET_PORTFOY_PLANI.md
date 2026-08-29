# Cloudlet — Portföy Sürümü Ürün ve Mimari Planı

**Tarih:** 26 Ağustos 2026  
**Ürün:** Cloudlet  
**Tanım:** Go, PostgreSQL ve S3-compatible object storage kullanan, self-hosted mini cloud storage uygulaması.

## 1. Ürün hedefi

Cloudlet tamamlandığında yalnızca dosya yükleyen bir API olmayacak. Kullanıcıların hesap açabildiği, dosya ve klasörlerini yönetebildiği, büyük dosyaları doğrudan object storage'a yükleyebildiği ve içeriklerini güvenli biçimde başkalarıyla paylaşabildiği küçük ama gerçek bir Google Drive alternatifi olacak.

Portföy sürümü aşağıdaki teknik yetkinlikleri gösterecek:

- Go ile production odaklı REST API geliştirme
- PostgreSQL veri modelleme ve transaction yönetimi
- AWS S3 / Cloudflare R2 / MinIO entegrasyonu
- Authentication ve kullanıcı bazlı authorization
- Multipart upload ve presigned URL kullanımı
- Kota, dosya versiyonlama ve paylaşım izinleri
- Docker, otomatik test, CI/CD ve gözlemlenebilirlik
- Modern ve responsive web arayüzü

## 2. Temel kullanıcı deneyimi

Kullanıcı aşağıdaki işlemleri yapabilecek:

1. Kayıt olmak ve giriş yapmak.
2. Kendisine ait dosya ve klasör alanını görüntülemek.
3. Dosya yüklemek, indirmek, yeniden adlandırmak, taşımak ve silmek.
4. İç içe klasörler oluşturmak.
5. Dosyaları aramak, sıralamak ve filtrelemek.
6. Büyük yüklemelerin ilerlemesini takip etmek ve yarıda kalan yüklemeyi sürdürmek.
7. Dosyaları linkle veya belirli Cloudlet kullanıcılarıyla paylaşmak.
8. Eski dosya sürümlerini görmek ve geri yüklemek.
9. Silinen dosyaları çöp kutusundan geri getirmek.
10. Kullanılan ve kalan depolama alanını görmek.

## 3. Sistem mimarisi

```text
Web istemcisi
      |
      | HTTPS / REST / JWT
      v
Cloudlet Go API
      |
      +-- PostgreSQL
      |     Kullanıcılar, metadata, klasörler, izinler,
      |     paylaşımlar, sürümler, kotalar ve oturumlar
      |
      +-- S3-compatible Object Storage
      |     Dosyaların gerçek binary içerikleri
      |
      +-- Arka plan görevleri
            Terk edilmiş upload temizliği, bakım,
            metadata-object tutarlılık kontrolleri
```

### Ortam tercihleri

- Yerel geliştirme: **MinIO + PostgreSQL + Docker Compose**
- Production object storage: öncelikli tercih **Cloudflare R2**
- Alternatif object storage: AWS S3, Backblaze B2 veya DigitalOcean Spaces
- Metadata ve ilişkisel veriler: **PostgreSQL**
- Backend: **Go**
- Frontend: modern bir web istemcisi

Uygulama S3-compatible API kullandığı için MinIO, R2 ve AWS S3 arasında minimum kod değişikliğiyle geçiş yapılabilecek.

## 4. Veri yerleşimi

Dosyanın gerçek içeriği PostgreSQL'e yazılmayacak. PostgreSQL yalnızca dosyayı tanımlayan ve yöneten verileri tutacak.

| Veri | Saklandığı yer |
|---|---|
| Dosya içeriği | MinIO / Cloudflare R2 / AWS S3 |
| Dosya adı, türü ve boyutu | PostgreSQL |
| Dosya sahibi | PostgreSQL |
| Klasör ve hiyerarşi | PostgreSQL |
| Object key | PostgreSQL |
| Paylaşım ve izinler | PostgreSQL |
| Dosya sürümleri | PostgreSQL + object storage |
| Kullanıcı, plan ve kota | PostgreSQL |
| Parola | PostgreSQL'de yalnızca bcrypt hash |
| Refresh token | PostgreSQL'de yalnızca güvenli hash |

Planlanan object key yapısı:

```text
users/{user-id}/files/{file-id}/versions/{version-id}
```

## 5. Kullanıcı ve oturum sistemi

- Kullanıcı kaydı ve giriş
- Bcrypt ile parola hash'leme
- Kısa ömürlü JWT access token
- Refresh token rotasyonu
- Logout ve tüm cihazlardan çıkış
- Aktif oturumları görüntüleme ve sonlandırma
- E-posta doğrulama
- Parola değiştirme ve parola sıfırlama
- Hesap kapatma
- Her kullanıcının verisini diğer kullanıcılardan kesin biçimde ayırma

Kullanıcı kimliği hassas dosya işlemlerinde istek gövdesinden alınmayacak; doğrulanmış token'dan çıkarılacak.

## 6. Dosya yönetimi

- Dosya yükleme ve indirme
- Dosya listeleme ve detay görüntüleme
- Yeniden adlandırma
- Klasörler arasında taşıma
- Soft delete
- Çöp kutusundan geri yükleme
- Kalıcı silme
- Toplu seçme ve toplu işlem
- Ada, türe, boyuta ve tarihe göre arama/filtreleme
- Oluşturulma tarihi, ad ve boyuta göre sıralama
- Aynı klasörde aynı isimli dosyalar için çakışma yönetimi
- Dosya bütünlüğü için checksum

## 7. Klasör sistemi

- Kullanıcıya ait kök dizin
- Klasör oluşturma
- Sınırsız olmasa da kontrollü iç içe klasör yapısı
- Klasör yeniden adlandırma
- Klasör taşıma
- Alt klasörlerle birlikte silme ve geri yükleme
- Breadcrumb navigasyonu
- Bir klasörün kendi altına taşınmasını engelleyen döngü kontrolü
- Klasör bazlı paylaşım için genişletilebilir veri modeli

## 8. Büyük dosya ve multipart upload

Küçük dosyalar API üzerinden yüklenebilir. Büyük dosyalarda backend'in bant genişliği darboğazı olmaması için istemci doğrudan object storage ile konuşacak.

Planlanan akış:

```text
İstemci -> Cloudlet: multipart upload başlat
Cloudlet -> İstemci: upload kimliği ve presigned part URL'leri
İstemci -> Object Storage: parçaları doğrudan yükle
İstemci -> Cloudlet: yüklemeyi tamamla
Cloudlet -> Object Storage: multipart complete
Cloudlet -> PostgreSQL: metadata ve kota kesinleştirme
```

Özellikler:

- Presigned upload URL
- Multipart upload başlatma ve tamamlama
- Parça bazlı progress
- Başarısız parçaları yeniden deneme
- Yarıda kalan yüklemeyi sürdürme
- Upload iptali
- Terk edilmiş multipart upload'ları temizleme
- Yükleme başlamadan kota rezervasyonu
- Checksum ve bütünlük kontrolü

## 9. Private bucket ve dosya erişimi

Object storage bucket'ı **private** olacak. Kalıcı storage URL'sini bilen bir kişinin Cloudlet authorization kontrollerini atlamasına izin verilmeyecek.

Normal indirme akışı:

```text
Kullanıcı -> Cloudlet API: dosyayı indir
Cloudlet: JWT ve dosya sahipliği kontrolü
Cloudlet: kısa süreli presigned URL üretir
Kullanıcı -> Object Storage: dosyayı doğrudan indirir
```

Presigned URL:

- Yalnızca tek bir object için geçerli olacak.
- Kısa süre sonra sona erecek.
- Bucket'taki diğer dosyalara erişim vermeyecek.
- AWS/R2 erişim anahtarlarını kullanıcıya göstermeyecek.
- Dosyanın Cloudlet sunucusundan geçmesini gerektirmeyecek.

Bu model authorization kontrolünü Cloudlet'te tutarken büyük dosyaların transferini object storage altyapısına bırakacak.

## 10. Dosya paylaşımı

İki paylaşım türü bulunacak:

### Link ile paylaşım

Hesabı olmayan kişiler de aşağıdaki gibi tahmin edilmesi zor bir bağlantıyla erişebilecek:

```text
https://cloudlet.app/s/{random-token}
```

Link doğrudan S3/R2 adresi olmayacak. Cloudlet paylaşım kaydını doğruladıktan sonra 1-5 dakikalık presigned download URL üretecek.

Paylaşım seçenekleri:

- Süreli veya süresiz bağlantı
- Parola koruması
- Maksimum indirme sayısı
- Görüntüleme/indirme izni
- Linki istenilen anda iptal etme
- Görüntüleme ve indirme sayacı

### Kullanıcıyla paylaşım

- Belirli Cloudlet kullanıcısıyla paylaşma
- Görüntüleyebilir veya düzenleyebilir izinleri
- “Benimle paylaşılanlar” ekranı
- İzni değiştirme veya kaldırma

Paylaşım token'ının kendisi veritabanında saklanmayacak; yalnızca hash'i tutulacak. Paylaşım parolaları da bcrypt veya uygun bir parola hash algoritmasıyla korunacak.

Cloudlet erişim vermeden önce şunları kontrol edecek:

1. Paylaşım mevcut mu?
2. Dosya veya sahibi silinmiş/pasif mi?
3. Paylaşım iptal edilmiş mi?
4. Son kullanma tarihi geçmiş mi?
5. İndirme limiti dolmuş mu?
6. Gerekliyse parola doğru mu?
7. İstenen işlem izin seviyesiyle uyumlu mu?

## 11. Dosya versiyonlama

- Aynı dosyanın yeni sürümünü yükleme
- Sürüm geçmişini listeleme
- Her sürüm için ayrı immutable object
- Eski sürümü indirme
- Eski sürüme geri dönme
- Belirli bir sürümü silme
- Sürümlerin tamamını kullanıcı kotasına dahil etme

## 12. Depolama kotası

İlk ürün kararı:

- Kullanıcı başına varsayılan kota: **5 GB**
- Varsayılan maksimum tek dosya boyutu: **100 MB**
- Büyük dosya desteği multipart upload aşamasında genişletilecek.

Gelecekte değerlendirilebilecek planlar:

| Plan | Depolama | Maksimum tek dosya |
|---|---:|---:|
| Free | 5 GB | 100 MB |
| Plus | 50 GB | 2 GB |
| Pro | 200 GB | 10 GB |

Kota kuralları:

- Dosya sürümleri kotaya dahil olacak.
- Çöp kutusundaki dosyalar kalıcı silinene kadar kotaya dahil olacak.
- Upload başlamadan önce yeterli alan kontrol edilecek.
- Eşzamanlı upload'lar için transaction tabanlı alan rezervasyonu kullanılacak.
- Kota aşımında API `409 storage_quota_exceeded` döndürecek.
- Arayüz kullanılan ve kalan alanı gösterecek.
- Yetim object'ler bakım göreviyle tespit edilecek.

## 13. Siber güvenlik modeli

### Uygulanacak temel kontroller

- Bcrypt parola hash'leme
- Güçlü parola politikası
- JWT imza algoritması ve süre doğrulaması
- En az 32 karakterli ve döndürülebilir JWT secret
- Refresh token rotasyonu ve token iptali
- Kullanıcı bazlı authorization
- Parametreli SQL sorguları
- İstek ve dosya boyutu sınırları
- Güvenli hata yanıtları; dahili DB ve storage hatalarını gizleme
- Dosya adlarında path traversal temizliği
- UUID tabanlı ve kullanıcıdan gizli object key'ler
- Private bucket ve kısa süreli presigned URL
- Rate limiting ve login brute-force koruması
- CORS politikası ve güvenli HTTP başlıkları
- Production ortamında TLS
- S3 server-side encryption
- Secret rotation
- Audit log

### Dosya güvenliği

- Dosya uzantısına güvenmeden içerikten MIME doğrulaması
- İzin verilen/yasaklanan dosya türü politikası
- Zararlı yazılım taraması
- Checksum doğrulaması
- Şüpheli dosyaları karantinaya alma
- Kullanıcı ve IP bazlı anomali/rate takibi

### Yetkilendirme ilkesi

Dosya sorguları hem dosya hem kullanıcı kimliğiyle sınırlandırılacak:

```sql
WHERE id = $1 AND owner_id = $2
```

Başka kullanıcıya ait bir dosyanın UUID'sini bilen kişi bile dosyayı göremeyecek veya presigned URL üretemeyecek. Kaynak varlığını sızdırmamak için uygun durumlarda `404` yanıtı kullanılacak.

## 14. Web arayüzü

- Kayıt, giriş ve parola sıfırlama ekranları
- Google Drive benzeri sade dosya/klasör görünümü
- Grid ve liste görünümü
- Sürükle-bırak upload
- Upload progress paneli
- Dosya işlemleri menüsü
- Arama, filtreleme ve sıralama
- Breadcrumb navigasyonu
- Paylaşım ayarları modalı
- Çöp kutusu
- Dosya sürüm geçmişi
- Depolama kullanım göstergesi
- Responsive mobil tasarım
- Karanlık tema
- Erişilebilir klavye navigasyonu

## 15. API kalitesi ve gözlemlenebilirlik

- OpenAPI 3 spesifikasyonu
- Swagger UI
- Tutarlı JSON hata formatı
- API versioning
- Sayfalama standardı
- Request ID
- Structured logging
- Graceful shutdown
- Health, readiness ve liveness endpoint'leri
- Temel metrikler: istek süresi, hata oranı, upload boyutu ve storage hataları

## 16. Test stratejisi

- Domain/service unit testleri
- HTTP handler testleri
- Authentication ve authorization testleri
- PostgreSQL repository entegrasyon testleri
- MinIO/S3 entegrasyon testleri
- Kullanıcılar arası veri izolasyonu testleri
- Multipart upload, retry ve iptal testleri
- Kota ve eşzamanlı upload yarış durumu testleri
- Paylaşım süresi, parola ve indirme limiti testleri
- Frontend component testleri
- Playwright ile uçtan uca kullanıcı senaryoları

Kritik izolasyon senaryosu:

```text
Kullanıcı A dosya yükler
Kullanıcı B dosyayı listeleyemez
B doğrudan dosya ID'sini denese de 404 alır
A paylaşım linki oluşturursa B izinler ölçüsünde erişebilir
```

## 17. Docker, CI/CD ve deployment

- Backend ve frontend Dockerfile
- PostgreSQL ve MinIO içeren Docker Compose geliştirme ortamı
- Environment validation
- GitHub Actions ile test, vet/lint ve build
- Production migration süreci
- Reverse proxy ve HTTPS
- Object storage lifecycle policy
- Veritabanı yedekleme yaklaşımı
- Log ve metrik toplama
- Güvenli secret yönetimi
- Tek komutla yerel kurulum

## 18. Portföy sunumu

- Profesyonel README
- Sistem mimarisi diyagramı
- Veritabanı ER diyagramı
- OpenAPI dokümantasyonu ve kullanım örnekleri
- Ekran görüntüleri
- Kısa demo videosu
- Teknik karar kayıtları
- Güvenlik ve tehdit modeli özeti
- Yerel kurulum rehberi
- Canlı demo veya demo kullanıcı
- Roadmap ve bilinen sınırlamalar

## 19. Teslim aşamaları

### Aşama 1 — Backend MVP

- Auth ve kullanıcı izolasyonu
- Dosya upload/download/list/detail
- Silme ve yeniden adlandırma
- Klasör sistemi
- Temel paylaşım linkleri
- Kota kontrolü

### Aşama 2 — Gelişmiş depolama

- Presigned upload
- Multipart upload
- Upload progress/resume/cancel
- Dosya versiyonlama
- Çöp kutusu

### Aşama 3 — Production güvenliği

- Refresh token ve session yönetimi
- Rate limiting
- Dosya doğrulama ve zararlı yazılım taraması
- Audit log
- TLS, encryption ve secret yönetimi

### Aşama 4 — Web ürünü

- Tam dosya/klasör arayüzü
- Paylaşım, versiyon ve çöp kutusu ekranları
- Responsive ve erişilebilir tasarım

### Aşama 5 — Portföy teslimi

- Entegrasyon ve E2E testleri
- CI/CD
- Deployment
- Dokümantasyon, diyagramlar ve demo

## 20. Nihai sonuç

Cloudlet'in portföy sürümü tamamlandığında elimizde şu ürün olacak:

> Kullanıcı hesapları, private object storage, büyük dosya yükleme, klasörler, paylaşım linkleri, izinler, dosya versiyonları, çöp kutusu, kota yönetimi ve modern web arayüzü bulunan; test edilmiş, Docker ile çalıştırılabilen ve production mimarisine göre tasarlanmış self-hosted mini cloud storage.
