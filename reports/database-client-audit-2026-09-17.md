# Veritabanı istemcisi denetimi — 17 Eylül 2026

## Kapsam ve kanıt düzeyi

Denetlenen sürüm: `9f2695b` çalışma ağacı; önceki yerel değişiklikler korunmuştur. `scripts/audit/run.py` her engine'i kendi sabit imajıyla, Docker'ın 4 GB sınırına uygun olarak sırayla çalıştırır. Ham `go test -json` olayları ve test özeti [`audit-evidence/`](audit-evidence/) altındadır. SQL testleri gerçek veritabanına karşı engine katmanını ve Studio'nun kullandığı HTTP API'yi (`httptest` sunucusu) çalıştırır. MongoDB/ClickHouse ve diğer sunucular için mevcut engine testleri daha dar bir temsilî senaryoyu kapsar. SQLite/DuckDB testleri geçici dosya kullanır. SQLite ve PostgreSQL için ayrıca tarayıcı E2E akışları çalıştırıldı. **Diğer 12 engine'de tarayıcı etkileşimi, Snowflake canlı bağlantısı, TLS/SSH fixture'ları, yetki reddi ve kaynak ölçümü yapılmadı.** Bunlar doğrulanmış kabul edilmemelidir.

İşaretler: ✅ doğrulanan işlem; ⚠️ kısmi/koşullu; ❌ uygulama eksikliği; N/A veritabanı kavramı uygulanmaz; ? doğrulanmadı. Son ek `L`: canlı test, `F`: geçici dosya testi, `K`: kod/arayüz incelemesi. `✅K` çalışan canlı davranış iddiası değildir. Çalışmayan ilk koşular (sandbox ağ yasağı, mevcut demo container parolaları ve erken Cassandra CQL başlatması) ürün hatası sayılmadı; ham nihai kanıt temiz fixture koşularından alınmıştır.

## Denetim sonrası uygulama durumu

- SQL Server composite FK DDL'si artık sütunları constraint sırasıyla tek tanımda üretiyor. Index metadata'sı key ve INCLUDE sütunlarını ayırıyor; DDL filtreyi, INCLUDE sütunlarını, sıralamayı, partition scheme/filegroup yerleşimini, ROW/PAGE compression ve yaygın index seçeneklerini koruyor. Üretilen DDL canlı SQL Server fixture'ında yeniden çalıştırıldı. Columnstore/spatial/XML index türleri yeniden oluşturma gerektiren yorumla gösteriliyor; bu özel türlerin DDL'si hâlâ eksik. [Kanıt](audit-evidence/mssql.jsonl).
- SQL Server gerçek planı `SET STATISTICS XML` ile alınıyor; aynı canlı pakette estimated ve actual plan testleri geçti. [Kanıt](audit-evidence/mssql.jsonl).
- Bağlantı havuzu ve SSH kurulumu global mutex dışında yapılıyor; aynı bağlantıya gelen istekler birleşiyor. Şema zenginleştirme sorguları en fazla iki eşzamanlı okuma ve 15 saniye bütçesiyle çalışıyor. 500 tablo ve 2.500 kolonlu PostgreSQL fixture'ı 120 ms'de, metadata uyarısı olmadan yüklendi. [Kanıt](audit-evidence/postgres.jsonl).
- Snowflake için FK, hybrid index, sequence ve procedure metadata sorguları eklendi; [Snowflake kataloğu](https://docs.snowflake.com/en/sql-reference/info-schema) ile statik olarak karşılaştırıldı. Hesap bulunmadığı için canlı sonuç **doğrulanmadı**. `INDEX_COLUMNS` yalnız desteklenen ticari AWS/Azure bölgelerinde mevcut; diğer bölgelerde metadata uyarısı beklenir.
- SQLite tarayıcı testi oturum, sorgu/sonuç, transaction rollback ve sekme açma/kapatmayı doğruluyor ([kayıt](audit-evidence/browser-sqlite.txt)). PostgreSQL tarayıcı testi grid edit, sorgu iptali, çalışan sorgu sekmesini kapatma ve Docker restart sonrası yeniden bağlanmayı doğruluyor ([kayıt](audit-evidence/browser-postgres.txt)). SQLite sorgularında sütun kökeni gelmediği için grid edit düğmesi pasif; bu sınır ayrıca test ediliyor.
- Explorer ağacı `ExplorerPanel.tsx` içine taşındı: ağacı açıp kapatmak artık editörün üst seviye state'ini değiştirmiyor. Kapanmış sekmeye geç gelen plan/multi-run sonuçları engelleniyor. 100.000 satırlık sayısal grid sıralamasında sayısal anahtarları bir kez hazırlama ölçümü [benchmark kaydında](audit-evidence/grid-benchmark.txt); filtre taraması düşük maliyetli kaldığı için ayrıca değiştirilmedi.

## Engine sonuçları

| Engine | Nihai kayıt | Temsilî canlı sonuç |
| --- | --- | --- |
| PostgreSQL | [JSON](audit-evidence/postgres.json) · [ham](audit-evidence/postgres.jsonl) | plan, transaction, API nesneleri, tip/backup/import/export, 100.000 satır ✅ |
| MySQL | [JSON](audit-evidence/mysql.json) · [ham](audit-evidence/mysql.jsonl) | aynı SQL paketi ✅ |
| MariaDB | [JSON](audit-evidence/mariadb.json) · [ham](audit-evidence/mariadb.jsonl) | plan, transaction ve API paketi ✅; ayrı 100.000 satır engine testi yok |
| SQL Server | [JSON](audit-evidence/mssql.json) · [ham](audit-evidence/mssql.jsonl) | aynı SQL paketi ✅ |
| CockroachDB | [JSON](audit-evidence/cockroachdb.json) · [ham](audit-evidence/cockroachdb.jsonl) | bağlantı, şema, yaz/oku ✅ |
| SQLite | [JSON](audit-evidence/sqlite.json) · [ham](audit-evidence/sqlite.jsonl) | geçici dosya, şema, DDL, transaction rollback ✅ |
| DuckDB | [JSON](audit-evidence/duckdb.json) · [ham](audit-evidence/duckdb.jsonl) | geçici dosya, şema, DDL, hassas sayı ✅ |
| ClickHouse | [JSON](audit-evidence/clickhouse.json) · [ham](audit-evidence/clickhouse.jsonl) | bağlantı, şema, sayı tipleriyle SELECT ✅ |
| MongoDB | [JSON](audit-evidence/mongodb.json) · [ham](audit-evidence/mongodb.jsonl) | bağlantı, şema, find/projection/skip ✅ |
| Redis | [JSON](audit-evidence/redis.json) · [ham](audit-evidence/redis.jsonl) | beş anahtar tipi, TTL, SCAN ✅ |
| Valkey | [JSON](audit-evidence/valkey.json) · [ham](audit-evidence/valkey.jsonl) | Redis protokol testi aynı beş tip/TTL/SCAN ✅ |
| Cassandra | [JSON](audit-evidence/cassandra.json) · [ham](audit-evidence/cassandra.jsonl) | CQL keşif, yazma, import/export doğrulaması ✅ |
| Elasticsearch | [JSON](audit-evidence/elasticsearch.json) · [ham](audit-evidence/elasticsearch.jsonl) | indeks keşfi, `_search` ✅ |
| Snowflake | kayıt yok | canlı hesap yok; doğrulanmadı |


| Engine | Bağlantı | Nesne/metadata | Sorgu/oturum | Veri düzenleme | Güvenlik | IDE/arayüz |
| --- | --- | --- | --- | --- | --- | --- |
| PostgreSQL | ✅L | ✅L | ✅L | ✅L | ⚠️K | ⚠️K |
| MySQL | ✅L | ✅L | ✅L | ✅L | ⚠️K | ⚠️K |
| MariaDB | ✅L | ✅L | ✅L | ✅L | ⚠️K | ⚠️K |
| SQL Server | ✅L | ✅L | ✅L | ✅L | ⚠️K | ⚠️K |
| CockroachDB | ✅L | ✅L | ✅L | ⚠️L (SQL yazma; grid doğrulanmadı) | ⚠️K | ⚠️K |
| SQLite | ✅F | ✅F | ✅F | ✅F | N/A (uzak TLS/SSH) | ⚠️K |
| DuckDB | ✅F | ✅F | ✅F | ⚠️K | N/A (uzak TLS/SSH) | ⚠️K |
| ClickHouse | ✅L | ✅L | ✅L | ⚠️K | ⚠️K | ⚠️K |
| MongoDB | ✅L | ✅L | ✅L (find) | ❌K (belge yazma) | ⚠️K | ⚠️K |
| Redis | ✅L | ✅L (anahtar tipleri) | ✅L (SCAN) | ❌K (anahtar yazma) | ⚠️K | ⚠️K |
| Valkey | ✅L (Redis protokolü) | ✅L (Redis protokolü) | ✅L (SCAN) | ❌K | ⚠️K | ⚠️K |
| Cassandra | ✅L | ✅L | ✅L (CQL) | ✅L (CQL yazma/import; grid doğrulanmadı) | ⚠️K | ⚠️K |
| Elasticsearch | ✅L | ✅L (indeks) | ✅L (_search) | ❌K (belge yazma) | ⚠️K | ⚠️K |
| Snowflake | ? | ? | ? | ? | ⚠️K | ⚠️K |

Canlı kanıtın tam sınırları: PostgreSQL/MySQL/SQL Server için tip matrisi ve 100.000 satır stream; dört ana SQL engine için plan, transaction, HTTP nesne keşfi, DDL, tip uçları, yedek/geri yükleme, CSV/JSON export ve import, büyük tablo alt testleri. CockroachDB bağlantı/şema/CREATE/INSERT/SELECT; ClickHouse bağlantı/şema/sorgu; MongoDB bağlantı/koleksiyon/find; Redis ve Valkey anahtar tipleri/TTL/SCAN; Cassandra keyspace/tablo keşfi, CQL SELECT/INSERT/UPDATE/batch/DDL, CSV import ve CQL export doğrulaması; Elasticsearch indeks/_search. Valkey testi Redis sürücüsünün Valkey sunucusuyla protokol uyumunu doğrular; ayrı Valkey UI akışını doğrulamaz. [Test fonksiyonları](../rowset-core/internal/engine/newengines_test.go), [SQL API fixture'ları](../rowset-core/internal/api/alltypes_live_test.go).

| Ayrı senaryo | Sonuç | Kanıt/sınır |
| --- | --- | --- |
| Hatalı kimlik bilgisi | ? | Hazır demo container parolası uyuşmazlığı görüldü; kontrollü negatif vaka değildi. |
| Yetki reddi | ? | Ayrı en az yetkili kullanıcı fixture'ı yok. |
| Timeout/iptal | ⚠️L | PostgreSQL tarayıcı testi `pg_sleep` sorgusunu durdurup sonraki sorguyu çalıştırdı; ağ timeout çeşitleri ölçülmedi. |
| Eşzamanlı sekmeler | ⚠️L | SQLite sekme açma/kapatma ve PostgreSQL çalışan sorgu sekmesini kapatma doğrulandı; iki eşzamanlı sorgu doğrulanmadı. |
| Büyük şema/sonuç | ⚠️L | 500 tablo/2.500 kolon metadata 120 ms, 100.000 satır stream ve [grid sıralama ölçümü](audit-evidence/grid-benchmark.txt); büyük sonuç tarayıcı render süresi ölçülmedi. |
| TLS/SSH | ⚠️K | [TLS canlı testi](../rowset-core/internal/engine/tls_live_test.go) ve SSH testleri mevcut, sertifikalı fixture bu koşuda kurulmadı. |
| Yeniden başlama/kopma | ⚠️L | PostgreSQL Docker restart sonrası aynı tarayıcı sekmesinde sorgu tekrar başarılı; diğer engine'ler doğrulanmadı. |
| Kaynak kullanımı | ? | Docker 4 GB sınırı altında sırayla çalıştırıldı; RSS/CPU eğrisi toplanmadı. |
| Snowflake | ? | Hesap yok; derleme ve DSN incelemesi canlı sonuç sayılmadı. |

## Bulgular ve ilk düzelteceğim 10 sorun

`Critical` düzeyinde kanıtlanmış kusur yok. İlk üç `High` kayıt uygulamanın veritabanının yapabildiği veri işlemlerini sunmamasıdır; canlı testler yalnızca mevcut okuma yolunu kanıtlar. Dördüncü kayıt Snowflake canlı doğrulama boşluğudur. Reprodüksiyon adımları yerel fixture ve Studio arayüzü içindir; bu listedeki MongoDB, Redis/Valkey ve Elasticsearch UI eksikleri tarayıcıyla tekrar edilmedi.

1. **High — MongoDB belge yazma yok.** Etki: MongoDB. Mevcut: koleksiyon gezgini/find var, belge ekleme/güncelleme/silme eylemi yok ([ConnectionForm](../rowset-studio/src/features/connections/ConnectionForm.tsx), [MongoFind](../rowset-core/internal/engine/mongodb.go)). Beklenen: güvenli, yetki/politika denetimli CRUD. Üretim: Mongo bağlantısı oluştur, koleksiyonu aç, bir belgeyi değiştir; yazma kontrolü yok. Neden: API ve engine yalnızca find yolunu sunuyor; grid düzenleme de Mongo için kapalı. En küçük düzeltme: tek belge CRUD endpoint'leri ve kimlik temelli grid eylemleri. Regresyon: canlı Mongo insert/update/delete, read-only bağlantıda ret, audit kaydı.
2. **High — Redis/Valkey anahtar yazma yok.** Etki: Redis, Valkey. Mevcut: SCAN ve tip önizlemesi çalışıyor, SET/HSET/DEL/TTL değiştirme akışı yok ([RedisScan](../rowset-core/internal/engine/redis.go), [ConnectionForm](../rowset-studio/src/features/connections/ConnectionForm.tsx)). Beklenen: tip bazlı kontrollü düzenleme. Üretim: iki engine'de anahtar açıp değer/TTL değiştirmeyi dene. Neden: yalnız okuma endpoint'i. En küçük düzeltme: önce string/hash için yazma API'si, politika ve salt okunur denetimi. Regresyon: iki gerçek sunucuda yaz/oku/TTL ve yetki reddi.
3. **High — Elasticsearch belge yazma yok.** Etki: Elasticsearch. Mevcut: indeks keşfi ve `_search`, belge oluşturma/güncelleme/silme yok ([ElasticsearchSearch](../rowset-core/internal/engine/elasticsearch.go), [ConnectionForm](../rowset-studio/src/features/connections/ConnectionForm.tsx)). Beklenen: belge kimliğiyle kontrollü CRUD. Üretim: indeks aramasında sonucu açıp düzenleme eylemi ara. Neden: arama endpoint'i dışında veri API'si yok. En küçük düzeltme: tek belge API'si ve arayüz eylemleri. Regresyon: canlı indeksleme, güncelleme, silme ve read-only ret.
4. **High — Snowflake için canlı doğrulama boşluğu.** Etki: Snowflake. Mevcut: bağlantı/DSN ve sorgu yolu kodda var, hesapla çalıştığı kanıtlanmadı ([connectionString](../rowset-core/internal/engine/engine.go), [capabilities](../rowset-core/internal/engine/capabilities.go)). Beklenen: hesapta bağlantı, keşif, sorgu, oturum, hata ve TLS senaryoları. Üretim: test hesabıyla `scripts/audit/run.py` kapsamı dışında manuel bağlan; bugün otomatik fixture yok. Neden: hesap/kimlik fixture'ı eksik. En küçük düzeltme: sır saklama kullanan opt-in Snowflake entegrasyon testi. Regresyon: CI'da koşan canlı bağlantı/metadata/query testi ve başarısız kimlik testi.
5. **Medium — Elasticsearch sorgusu sayfalama ve tüm arama DSL'sini taşımıyor.** Etki: Elasticsearch. Mevcut: API yalnız `query` ve `size` gövdesi kuruyor, `size<=10000`; `search_after`, sıralama ve aggregation sonucu yok ([ElasticsearchSearch](../rowset-core/internal/engine/elasticsearch.go)). Beklenen: büyük indeksleri eksiksiz gezme ve kullanıcı sorgu gövdesini destekleme. Üretim: 10.000'den fazla belge içeren indekste arama; sonraki sayfa alınamaz. Neden: istemci gövdesi ve dönüş tipi `hits` ile sınırlı. En küçük düzeltme: kontrollü `sort/search_after` ve aggregation yanıtı. Regresyon: 10.001 belge fixture'ında sayfa sınırı ve toplam kayıt.
6. **Medium — MongoDB aggregation yok.** Etki: MongoDB. Mevcut: filtre/sort/project/skip/limit ile `Find`; pipeline desteği yok ([MongoFind](../rowset-core/internal/engine/mongodb.go), [ConnectionForm](../rowset-studio/src/features/connections/ConnectionForm.tsx)). Beklenen: `$match/$group` gibi okuma pipeline'ları. Üretim: editöre aggregation JSON'u gir; find şeması kabul etmez. Neden: tek Find endpoint'i. En küçük düzeltme: salt okunur pipeline endpoint'i ve tehlikeli aşama denetimi. Regresyon: canlı `$match/$group`, yasak `$out/$merge`.
7. **Medium — CockroachDB plan denetimi kapalı.** Etki: CockroachDB. Mevcut: `Explain=false` ve ortak plan yolu engine'i reddediyor ([capabilities](../rowset-core/internal/engine/capabilities.go), [explain](../rowset-core/internal/engine/explain.go)). Beklenen: SQL plan metnini görüntüleme. Üretim: Cockroach bağlantısında SQL SELECT ve plan eylemi; kontrol görünmez. Neden: plan formatı entegrasyonu yok. En küçük düzeltme: Cockroach EXPLAIN sonucu için ayrı biçimleme ve capability. Regresyon: canlı SELECT planı, yazan sorguda ANALYZE güvenlik denetimi.
8. **Medium — ClickHouse plan denetimi kapalı.** Etki: ClickHouse. Mevcut: DDL var, `Explain=false`; plan endpoint'i desteklemiyor ([capabilities](../rowset-core/internal/engine/capabilities.go), [explain](../rowset-core/internal/engine/explain.go)). Beklenen: temel SELECT planı. Üretim: ClickHouse SELECT yaz, plan eylemini açmayı dene. Neden: engine özel plan adaptörü yok. En küçük düzeltme: ClickHouse EXPLAIN sonucu ve arayüz sunumu. Regresyon: canlı plan ve hata/timeout testi.
9. **Medium — SQLite/DuckDB CSV içe aktarma kontrolü yok.** Etki: dosya engine'leri. Mevcut: sorgu/DDL ve export var; capability `CSVImport=false` ([capabilities](../rowset-core/internal/engine/capabilities.go)). Beklenen: seçilen tabloya denetimli CSV yükleme. Üretim: geçici dosya DB'de tablo seç, içe aktarma eylemini ara. Neden: import servisi bu engine'lere bağlanmamış. En küçük düzeltme: ortak import akışını dosya transaction'ıyla uyarlama. Regresyon: iki geçici dosyada tip dönüşümü, hata halinde rollback.
10. **Low — Şema karşılaştırma seçimi uyumsuz engine'leri gösteriyor.** Etki: Redis, Valkey, Elasticsearch ve muhtemelen Cassandra. Mevcut: bağlantı seçimi yalnız MongoDB'yi filtreliyor ([SchemaComparePage](../rowset-studio/src/features/editor/SchemaComparePage.tsx)); motorların metadata modelleri farklı. Beklenen: destekli karşılaştırmaların açık listesi veya engine'e özel fark çıktısı. Üretim: bu bağlantılardan birini oluşturup Schema Compare seçicisini aç. Neden: tek engine hariç tutma koşulu. En küçük düzeltme: capability tabanlı seçim ve desteklenmeyen çiftte açıklama. Regresyon: seçicide her 14 engine için görünürlük testi.

## Tekrar çalıştırma

```sh
# Repo kökünde; Docker imajları images.lock.json içinde digest ile sabitlenmiştir.
python3 scripts/audit/run.py all
python3 scripts/audit/run.py postgres

# Var olan localhost lab için ilgili ROWSET_* parolasını/host değişkenini verin:
ROWSET_TEST_MONGODB_HOST=127.0.0.1 python3 scripts/audit/run.py mongodb --existing

(cd rowset-studio && npm run build && npm run lint && npm test)
GOCACHE=/tmp/rowset-audit-gocache sh scripts/build-local-binary.sh
(cd rowset-studio && npm run test:e2e)
(cd rowset-studio && npm run test:e2e:postgres)
node --experimental-strip-types rowset-studio/scripts/bench-grid.mjs
```

`all` başarısız/atlanan seçili testi sıfır durum koduyla kapatmaz; engine özet JSON'u ve ham JSONL raporda tutulur. Varsayılan mod yalnız `rowset-audit-*` adlı geçici container'ları oluşturur ve kaldırır. Gerekli port başka container tarafından kullanılıyorsa önce onu durdurup sonra geri başlatın; var olan container'ın verisi silinmez. Testler fixture tabloları oluşturur; bu nedenle üretim bağlantısında `--existing` kullanmayın. SQL Server fixture'ı test veritabanını oluşturur. TLS/SSH ve Snowflake için ayrıca kimlik/sertifika fixture'ı gereklidir. Kaynak kullanım eğrisi ve tüm engine'lerde tarayıcı E2E kapsamı açık boşluklardır.
