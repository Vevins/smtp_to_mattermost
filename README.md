# smtp_to_mattermost

`smtp_to_mattermost` — это небольшой SMTP-сервер, который принимает входящие email-сообщения и пересылает их в каналы Mattermost.

Идея проекта такая же, как у [`smtp_to_telegram`](https://github.com/KostyaEsmukov/smtp_to_telegram): если какая-то внешняя система умеет отправлять уведомления только по SMTP, можно направить их в этот сервис и получать сообщения в Mattermost вместо почты.

## Что умеет

- слушает SMTP на настраиваемом адресе
- принимает обычные email без TLS и SMTP-аутентификации
- извлекает тему, получателей, текст письма и вложения
- отправляет сообщение в один или несколько каналов Mattermost
- загружает вложения в Mattermost и прикрепляет их к посту
- обрезает слишком длинные сообщения и прикладывает полный текст как `full_message.txt`

## Как это работает

1. Ваше приложение отправляет email-уведомление в `smtp_to_mattermost`.
2. `smtp_to_mattermost` разбирает письмо: тело, тему, адресатов и вложения.
3. Вложения загружаются через Mattermost API.
4. В целевом канале создаётся пост с текстом и загруженными файлами.

## Почему через API, а не incoming webhook

Incoming webhooks в Mattermost подходят для простых текстовых сообщений, но для вложений надёжнее и правильнее использовать стандартный API-поток:

1. загрузить файлы
2. получить `file_ids`
3. создать пост с этими `file_ids`

Именно так этот проект и работает.

## Быстрый старт

Для запуска нужны:

- URL вашего Mattermost-сервера
- bot token или personal access token
- один или несколько `channel_id`, в которые нужно отправлять сообщения

Запуск контейнера:

```bash
docker run \
  --name smtp_to_mattermost \
  -p 2525:2525 \
  -e ST_SMTP_LISTEN=0.0.0.0:2525 \
  -e ST_MATTERMOST_SERVER_URL=https://mattermost.example.com \
  -e ST_MATTERMOST_TOKEN=<TOKEN> \
  -e ST_MATTERMOST_CHANNEL_IDS=<CHANNEL_ID1>,<CHANNEL_ID2> \
  vevin/smtp_to_mattermost
```

Или через `docker compose`:

```bash
cp .env.example .env
docker compose up --build
```

После этого приложение можно настроить на отправку уведомлений по адресу:

```text
smtp_to_mattermost:2525
```

Если контейнер запущен локально с проброшенным портом, можно использовать:

```text
localhost:2525
```

## Локальная разработка

Запуск локально через Go:

```bash
cp .env.example .env
go mod tidy
go test ./...
go run .
```

По умолчанию сервис слушает:

```text
127.0.0.1:2525
```

## Подготовка Mattermost

### 1. Создайте токен

Подойдут:

- bot account с bot token
- personal access token

У токена должны быть права на:

- загрузку файлов
- создание постов в нужных каналах

### 2. Добавьте бота в нужный канал

Пользователь или бот, от имени которого работает токен, должен состоять во всех целевых каналах.

### 3. Получите `channel_id`

Нужен именно внутренний идентификатор канала Mattermost, а не его человекочитаемое имя.

Обычно его можно получить:

- через devtools в браузере
- через Mattermost API
- из существующей интеграции или бота, где этот канал уже используется

Если каналов несколько, передайте их списком через запятую:

```text
ST_MATTERMOST_CHANNEL_IDS=channel-id-1,channel-id-2
```

## Конфигурация

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `ST_SMTP_LISTEN` | Адрес, на котором слушает SMTP-сервер | `127.0.0.1:2525` |
| `ST_SMTP_PRIMARY_HOST` | Primary host, который сообщает SMTP-сервер | hostname системы |
| `ST_SMTP_MAX_ENVELOPE_SIZE` | Максимальный размер входящего письма | `50m` |
| `ST_MATTERMOST_SERVER_URL` | Базовый URL Mattermost | обязательно |
| `ST_MATTERMOST_TOKEN` | Bot token или personal access token Mattermost | обязательно |
| `ST_MATTERMOST_CHANNEL_IDS` | Список `channel_id` через запятую | обязательно |
| `ST_MATTERMOST_MESSAGE_TEMPLATE` | Шаблон текста исходящего сообщения | `From: {from}\nTo: {to}\nSubject: {subject}\n\n{body}\n\n{attachments_details}` |
| `ST_MATTERMOST_API_TIMEOUT_SECONDS` | Таймаут запросов к Mattermost API | `30` |
| `ST_FORWARDED_ATTACHMENT_MAX_SIZE` | Максимальный размер пересылаемого вложения, `0` отключает пересылку | `10m` |
| `ST_MESSAGE_LENGTH_TO_SEND_AS_FILE` | Если итоговое сообщение длиннее этого значения, оно обрезается, а полный текст прикладывается как файл | `12000` |
| `ST_LOG_LEVEL` | Уровень логирования | `info` |

Пример `.env`:

```dotenv
ST_SMTP_LISTEN=0.0.0.0:2525
ST_SMTP_PRIMARY_HOST=localhost
ST_SMTP_MAX_ENVELOPE_SIZE=50m

ST_MATTERMOST_SERVER_URL=https://mattermost.example.com
ST_MATTERMOST_TOKEN=replace-me
ST_MATTERMOST_CHANNEL_IDS=channel-id-1,channel-id-2
ST_MATTERMOST_MESSAGE_TEMPLATE=From: {from}\nTo: {to}\nSubject: {subject}\n\n{body}\n\n{attachments_details}
ST_MATTERMOST_API_TIMEOUT_SECONDS=30

ST_FORWARDED_ATTACHMENT_MAX_SIZE=10m
ST_MESSAGE_LENGTH_TO_SEND_AS_FILE=12000
ST_LOG_LEVEL=info
```

## Шаблон сообщения

Шаблон по умолчанию:

```text
From: {from}\nTo: {to}\nSubject: {subject}\n\n{body}\n\n{attachments_details}
```

Поддерживаемые плейсхолдеры:

- `{from}`
- `{to}`
- `{subject}`
- `{body}`
- `{attachments_details}`

Пример кастомного шаблона:

```bash
docker run \
  --name smtp_to_mattermost \
  -p 2525:2525 \
  -e ST_SMTP_LISTEN=0.0.0.0:2525 \
  -e ST_MATTERMOST_SERVER_URL=https://mattermost.example.com \
  -e ST_MATTERMOST_TOKEN=<TOKEN> \
  -e ST_MATTERMOST_CHANNEL_IDS=<CHANNEL_ID> \
  -e ST_MATTERMOST_MESSAGE_TEMPLATE="Subject: {subject}\n\n{body}" \
  vevin/smtp_to_mattermost
```

## Вложения

- вложения меньше `ST_FORWARDED_ATTACHMENT_MAX_SIZE` загружаются в Mattermost
- если `ST_FORWARDED_ATTACHMENT_MAX_SIZE=0`, пересылка вложений отключается
- информация о вложениях всё равно добавляется в текст сообщения
- если итоговый текст слишком длинный, он обрезается, а полный вариант отправляется как `full_message.txt`

## SMTP-поведение

Сервис намеренно сделан простым:

- без SMTP-аутентификации
- без TLS
- принимает письмо и сразу пытается переслать его в Mattermost

Это мост между приложением и Mattermost, который обычно запускается во внутренней сети или Docker-сети.

## Проверка работы

Запуск тестов:

```bash
go test ./...
```

Тесты покрывают:

- успешную отправку
- кастомные шаблоны сообщений
- ошибки Mattermost API
- загрузку вложений
- обрезку длинных сообщений

## Сборка

Собрать бинарник:

```bash
go build -o smtp_to_mattermost
```

Собрать Docker-образ:

```bash
docker build -t smtp_to_mattermost .
```

## Примеры использования

- старые системы мониторинга, умеющие слать только email
- уведомления от NAS, роутеров и сетевого оборудования
- отчёты о бэкапах
- ошибки cron-задач
- уведомления от принтеров, ИБП и другой инфраструктуры
- CI/CD-инструменты, где есть только SMTP-уведомления

## Исходная идея

Проект вдохновлён:

- [KostyaEsmukov/smtp_to_telegram](https://github.com/KostyaEsmukov/smtp_to_telegram)

## Ссылки

- [Mattermost incoming webhooks](https://developers.mattermost.com/integrate/webhooks/incoming/)
- [Mattermost API documentation](https://api.mattermost.com/)
