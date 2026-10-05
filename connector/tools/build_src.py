"""Генерация XML-исходников расширения-коннектора MCP.

Кодировка UTF-8 с BOM, как пишет конфигуратор. Модуль HTTP-сервиса правится прямо в
src/HTTPServices/MCP_Сервис/Ext/Module.bsl, скрипт его не перезаписывает.

Версия формата выгрузки (атрибут version корневого тега) зависит от ВЕРСИИ ПЛАТФОРМЫ, а не
от режима совместимости, и на разных платформах она разная: 8.3.27.2130 грузит только
2.20 и отвергает 2.21. Поэтому версия не зашита: берётся из свежей выгрузки той конфигурации,
куда пойдёт расширение (--from-dump), либо задаётся явно (--format-version).

Прогон повторяем: UUID объектов и версия расширения читаются из уже собранного src, новые
генерируются только для того, чего там ещё нет. Поэтому пересборка не превращает загруженное
в базу расширение в другое, а изменения ограничены тем, что реально поменялось в описании.

Примеры:
    python3 build_src.py                                     # пересобрать по существующему src
    python3 build_src.py --from-dump <каталог выгрузки>      # первая сборка
    python3 build_src.py --format-version 2.21 --version 0.3.0.0
"""
import argparse
import pathlib
import re
import uuid

ROOT = pathlib.Path(__file__).resolve().parent.parent
SRC = ROOT / "src"


def текст(путь):
    return путь.read_text(encoding="utf-8-sig") if путь.exists() else ""


def описания_модулей(src):
    """XML-описания общих модулей: их пишет генератор, поэтому у нового модуля описания ещё нет."""
    return [src / f"CommonModules/{PREFIX}{имя}.xml" for имя, _ in COMMON_MODULES]


def ожидаемые_файлы(src):
    return [
        src / "Configuration.xml",
        src / "Languages/Русский.xml",
        src / f"HTTPServices/{SERVICE_NAME}.xml",
        src / f"HTTPServices/{SERVICE_NAME}/Ext/Module.bsl",
    ] + описания_модулей(src) + [
        src / f"CommonModules/{PREFIX}{имя}/Ext/Module.bsl" for имя, _ in COMMON_MODULES
    ]


def каталог_пуст(src):
    """Первой сборкой считается только отсутствующий или полностью пустой каталог.

    Смотреть на наличие «своих» файлов недостаточно: каталог с посторонним содержимым
    (уцелевшие объекты, чужие XML, остатки прошлой выгрузки) — это не чистый лист, и
    сборка поверх него дала бы расширение с новыми идентификаторами и лишним составом.
    """
    if not src.exists():
        return True
    if not src.is_dir():
        raise SystemExit(f"{src} существует, но это не каталог: уберите его перед сборкой")
    посторонние = [путь for путь in src.rglob("*") if путь.is_file()]
    return not посторонние


def прошлое_состояние(src):
    """UUID и версия расширения из уже собранного src: пересборка не должна их менять."""
    состояние = {"uuid": {}, "version": "", "format": "", "собран": not каталог_пуст(src)}
    конфигурация = текст(src / "Configuration.xml")
    if конфигурация:
        состояние["format"] = найти(r'version="(2\.\d+)"', конфигурация)
        состояние["version"] = найти(r"<Version>([^<]*)</Version>", конфигурация)
        состояние["uuid"]["configuration"] = найти(r'<Configuration uuid="([^"]+)"', конфигурация)
        for индекс, найдено in enumerate(re.findall(r"<xr:ObjectId>([^<]+)</xr:ObjectId>", конфигурация)):
            состояние["uuid"][f"contained:{индекс}"] = найдено
    язык = текст(src / "Languages/Русский.xml")
    if язык:
        состояние["uuid"]["language"] = найти(r'<Language uuid="([^"]+)"', язык)
    сервис = текст(src / f"HTTPServices/{SERVICE_NAME}.xml")
    if сервис:
        состояние["uuid"]["service"] = найти(r'<HTTPService uuid="([^"]+)"', сервис)
        for uuid_шаблона, тело in re.findall(r'<URLTemplate uuid="([^"]+)">(.*?)</URLTemplate>', сервис, re.S):
            имя = найти(r"<Name>([^<]+)</Name>", тело)
            состояние["uuid"][f"template:{имя}"] = uuid_шаблона
            состояние["uuid"][f"method:{имя}"] = найти(r'<Method uuid="([^"]+)"', тело)
    for имя, _ in COMMON_MODULES:
        модуль = текст(src / f"CommonModules/{PREFIX}{имя}.xml")
        if модуль:
            состояние["uuid"][f"module:{имя}"] = найти(r'<CommonModule uuid="([^"]+)"', модуль)
    return состояние


def последствия(потеряны, лишние, состояние):
    """Что скрипт запишет в src при --allow-new-uuid.

    Формулировки намеренно про файлы, а не про поведение платформы: как именно 1С поступит
    с объектом, у которого сменился идентификатор, зависит от способа загрузки, и гадать об
    этом в предупреждении нельзя. Достоверно известно одно: объект с новым uuid платформа
    считает другим объектом.
    """
    итог = []
    if "configuration" in потеряны:
        итог.append("новый uuid получит сама конфигурация расширения")
    служебные = [ключ for ключ in потеряны if ключ.startswith("contained:")]
    if служебные:
        итог.append(f"новые uuid получат служебные записи состава (ContainedObject): {len(служебные)} шт.")
    объекты = [ключ for ключ in потеряны if ключ in ("language", "service")]
    if объекты:
        итог.append("новые uuid получат: " + ", ".join(объекты))
    # Шаблон и его метод — разные объекты с разными uuid: потеря одного не трогает второй.
    шаблоны = sorted(ключ.split(":", 1)[1] for ключ in потеряны if ключ.startswith("template:"))
    if шаблоны:
        итог.append("новые uuid получат шаблоны URL: " + ", ".join(шаблоны))
    методы = sorted(ключ.split(":", 1)[1] for ключ in потеряны if ключ.startswith("method:"))
    if методы:
        итог.append("новые uuid получат методы шаблонов: " + ", ".join(методы))
    if итог:
        итог.append("объект с новым uuid платформа считает другим объектом,"
                    " прежний в расширении не сохранится")
    if лишние:
        итог.append("из состава расширения исчезнут шаблоны: " + ", ".join(лишние))
    if not состояние["version"]:
        откуда = f"из --version ({EXT_VERSION})" if АРГУМЕНТЫ.ext_version else f"по умолчанию ({EXT_VERSION})"
        итог.append(f"версия расширения будет записана {откуда}, uuid это не затрагивает")
    return итог


def проверить_состояние(состояние, разрешить_новые):
    """Обрыв на неполном src: иначе пересборка тихо выпишет новые UUID и подменит расширение.

    Отсутствие целого src — законная первая сборка. Отсутствие отдельного шаблона — новый
    эндпоинт. А вот пропавший UUID расширения, языка или самого сервиса означает, что src
    повреждён: молча пересобирать его нельзя, загруженное в базу расширение перестанет
    сопоставляться по идентификаторам.
    """
    if not состояние["собран"]:
        print("src пуст: первая сборка, все UUID новые")
        return

    претензии = []

    # Описание общего модуля генератор пишет сам: его отсутствие означает новый модуль, как
    # отсутствие шаблона означает новый эндпоинт. Текст модуля обязан лежать в src.
    генерируемые = set(описания_модулей(SRC))
    отсутствуют = [
        str(файл.relative_to(SRC)) for файл in ожидаемые_файлы(SRC)
        if not файл.exists() and файл not in генерируемые
    ]
    if отсутствуют:
        претензии.append("в src нет файлов: " + ", ".join(отсутствуют))

    ожидаемые = {файл.resolve() for файл in ожидаемые_файлы(SRC)}
    посторонние = sorted(
        str(путь.relative_to(SRC)) for путь in SRC.rglob("*")
        if путь.is_file() and путь.resolve() not in ожидаемые
    )
    if посторонние:
        # Это не про идентификаторы: генератор такие файлы не пишет и не удаляет, а при
        # загрузке расширения они станут частью его состава. Флагом не снимается.
        raise SystemExit(
            "в src есть посторонние файлы: " + ", ".join(посторонние) + ".\n"
            "Генератор ими не управляет, а в расширение они попадут. Уберите их или соберите\n"
            "в чистый каталог."
        )

    обязательные = ["configuration", "language", "service"]
    обязательные += [f"contained:{индекс}" for индекс in range(len(CONTAINED_CLASS_IDS))]
    обязательные += [f"template:{имя}" for имя, _, _, _ in ENDPOINTS]
    обязательные += [f"method:{имя}" for имя, _, _, _ in ENDPOINTS]
    потеряны = [ключ for ключ in обязательные if not состояние["uuid"].get(ключ)]
    if потеряны:
        претензии.append("не найдены UUID: " + ", ".join(потеряны))

    # Лишний шаблон в src означает, что эндпоинт удалили из описания: пересборка выбросит
    # его из расширения, и это тоже осознанное решение, а не побочный эффект.
    известные = {f"template:{имя}" for имя, _, _, _ in ENDPOINTS}
    лишние = sorted(
        ключ.split(":", 1)[1]
        for ключ in состояние["uuid"]
        if ключ.startswith("template:") and ключ not in известные
    )
    if лишние:
        претензии.append("в src есть шаблоны, которых нет в ENDPOINTS: " + ", ".join(лишние))

    if not состояние["version"]:
        претензии.append("в src не найдена версия расширения (тег Version)")

    if not претензии:
        return
    if разрешить_новые:
        for претензия in претензии:
            print("ВНИМАНИЕ:", претензия)
        for строка in последствия(потеряны, лишние, состояние):
            print("ВНИМАНИЕ:", строка)
        return
    raise SystemExit(
        "src собран, но неполон или повреждён:\n  " + "\n  ".join(претензии) + "\n"
        "Пересборка выписала бы новые идентификаторы или потеряла состав, и загруженное\n"
        "в базу расширение перестало бы сопоставляться. Восстановите src либо пересоберите\n"
        "осознанно:\n"
        "    python3 build_src.py --allow-new-uuid"
    )


def найти(шаблон, текст_файла):
    найдено = re.search(шаблон, текст_файла)
    return найдено.group(1) if найдено else ""


NS_FULL = (
    'xmlns="http://v8.1c.ru/8.3/MDClasses" '
    'xmlns:app="http://v8.1c.ru/8.2/managed-application/core" '
    'xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" '
    'xmlns:cmi="http://v8.1c.ru/8.2/managed-application/cmi" '
    'xmlns:ent="http://v8.1c.ru/8.1/data/enterprise" '
    'xmlns:lf="http://v8.1c.ru/8.2/managed-application/logform" '
    'xmlns:style="http://v8.1c.ru/8.1/data/ui/style" '
    'xmlns:sys="http://v8.1c.ru/8.1/data/ui/fonts/system" '
    'xmlns:v8="http://v8.1c.ru/8.1/data/core" '
    'xmlns:v8ui="http://v8.1c.ru/8.1/data/ui" '
    'xmlns:web="http://v8.1c.ru/8.1/data/ui/colors/web" '
    'xmlns:win="http://v8.1c.ru/8.1/data/ui/colors/windows" '
    'xmlns:xen="http://v8.1c.ru/8.3/xcf/enums" '
    'xmlns:xpr="http://v8.1c.ru/8.3/xcf/predef" '
    'xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" '
    'xmlns:xs="http://www.w3.org/2001/XMLSchema" '
    'xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"'
)
NS_SHORT = (
    'xmlns="http://v8.1c.ru/8.3/MDClasses" '
    'xmlns:app="http://v8.1c.ru/8.2/managed-application/core" '
    'xmlns:v8="http://v8.1c.ru/8.1/data/core" '
    'xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" '
    'xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"'
)

# ClassId служебных содержащихся объектов конфигурации (одинаковы у любой конфигурации),
# ObjectId генерируются свои, чтобы не пересекаться с другими расширениями базы.
CONTAINED_CLASS_IDS = [
    "9cd510cd-abfc-11d4-9434-004095e12fc7",
    "9fcd25a0-4822-11d4-9414-008048da11f9",
    "e3687481-0a87-462c-a166-9f34594f9bba",
    "9de14907-ec23-4a07-96f0-85521cb6b53b",
    "51f2d5d8-ea4d-4064-8892-82951750031e",
    "e68182ea-4237-4383-967f-90c1e3370bc7",
    "fb282519-d103-4dd3-bc12-cb271d631dfc",
]

EXT_NAME = "MCPКоннектор"
EXT_SYNONYM = "Коннектор MCP"

# Режим совместимости расширения обязан быть НЕ ВЫШЕ режима основной конфигурации, иначе
# «Проверка возможности применения» отказывает, и кнопкой «Исправить» это не лечится.
# 8.3.27 (от базы-донора УТ 11.5.22) не вставал в конфигурации с более старым режимом.
# Берём 8.3.14: с этим значением коннектор встаёт в большинство баз, а новее ему ничего не
# нужно, это HTTP-сервис на JSON. Ниже опускать смысла нет - на очень старых конфигурациях
# (УТ 11.3) расширение не встаёт вовсе, там HTTP-сервис добавляется нативно.
EXT_COMPAT = "Version8_3_14"
PREFIX = "MCP_"
SERVICE_NAME = PREFIX + "Сервис"
# Корневой URL mcp-1c: его ждёт Go-клиент (--base http://<хост>/<база>/hs/mcp-1c).
# Два расширения с одним корневым URL в одной базе конфликтуют.
ROOT_URL = "mcp-1c"

# Шаблон URL -> (адрес, HTTP-метод, обработчик из модуля сервиса).
# Порядок и состав должны совпадать с тем, что зовёт Go-клиент (internal/source/httpsource.go).
ENDPOINTS = [
    ("configuration", "/configuration", "GET", "КонфигурацияGET"),
    ("metadata", "/metadata", "GET", "МетаданныеGET"),
    ("object", "/object/{type}/{name}", "GET", "ОбъектGET"),
    ("query", "/query", "POST", "ЗапросPOST"),
    ("validatequery", "/validate-query", "POST", "ВалидацияЗапросаPOST"),
    ("eventlog", "/eventlog", "POST", "ЖурналPOST"),
    ("subsystem", "/subsystem/{name}", "GET", "ПодсистемаGET"),
    ("predefined", "/predefined/{type}/{name}", "GET", "ПредопределенныеGET"),
]

# Общие модули расширения: (имя без префикса, синоним). Серверные по стандарту 1С: флажки
# «Сервер», «Внешнее соединение» и «Клиент (обычное приложение)», без вызова сервера.
# Текст модуля лежит в src/CommonModules/<префикс><имя>/Ext/Module.bsl и правится там же.
# «Запросы» нужен ради фонового задания: оно вызывает только экспортные методы общих модулей.
COMMON_MODULES = [
    ("Запросы", "Запросы MCP"),
]


разбор = argparse.ArgumentParser(description=__doc__)
разбор.add_argument("--from-dump", help="каталог XML-выгрузки конфигурации, откуда взять версию формата")
разбор.add_argument("--format-version", help="версия формата выгрузки, например 2.20")
разбор.add_argument("--version", dest="ext_version", help="версия расширения, например 0.2.0.0")
разбор.add_argument("--allow-new-uuid", action="store_true",
                    help="разрешить новые UUID при неполном src (расширение в базе станет другим)")
АРГУМЕНТЫ = разбор.parse_args()
СОСТОЯНИЕ = прошлое_состояние(SRC)


def версия_формата():
    if АРГУМЕНТЫ.format_version:
        return АРГУМЕНТЫ.format_version
    if АРГУМЕНТЫ.from_dump:
        файл = pathlib.Path(АРГУМЕНТЫ.from_dump).expanduser() / "Configuration.xml"
        найдено = re.search(r'version="(2\.\d+)"', файл.read_text(encoding="utf-8-sig"))
        if not найдено:
            raise SystemExit(f"не нашёл version в {файл}")
        return найдено.group(1)
    if СОСТОЯНИЕ["format"]:
        return СОСТОЯНИЕ["format"]
    raise SystemExit("укажите --from-dump <каталог выгрузки> или --format-version <2.xx>")


VER = версия_формата()
EXT_VERSION = АРГУМЕНТЫ.ext_version or СОСТОЯНИЕ["version"] or "0.1.0.1"

# Проверка идёт после вычисления версий: она о них рассказывает.
проверить_состояние(СОСТОЯНИЕ, АРГУМЕНТЫ.allow_new_uuid)

ПЕРЕИСПОЛЬЗОВАНО = 0
СОЗДАНО = 0


def uid(ключ):
    """UUID из уже собранного src, иначе новый: пересборка сохраняет тождество объектов."""
    global ПЕРЕИСПОЛЬЗОВАНО, СОЗДАНО
    сохранённый = СОСТОЯНИЕ["uuid"].get(ключ)
    if сохранённый:
        ПЕРЕИСПОЛЬЗОВАНО += 1
        return сохранённый
    СОЗДАНО += 1
    if СОСТОЯНИЕ["собран"]:
        print(f"новый UUID для {ключ}")
    return str(uuid.uuid4())


def synonym(text, indent):
    pad = "\t" * indent
    return (
        f"{pad}<Synonym>\n"
        f"{pad}\t<v8:item>\n"
        f"{pad}\t\t<v8:lang>ru</v8:lang>\n"
        f"{pad}\t\t<v8:content>{text}</v8:content>\n"
        f"{pad}\t</v8:item>\n"
        f"{pad}</Synonym>"
    )


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    # newline задан явно: на Windows текстовый режим иначе превратит LF в CRLF, и пересборка
    # перепишет каждый файл целиком.
    path.write_text(text, encoding="utf-8-sig", newline="\n")
    print("записан", path)


def configuration_xml():
    contained = "\n".join(
        f"\t\t\t<xr:ContainedObject>\n"
        f"\t\t\t\t<xr:ClassId>{class_id}</xr:ClassId>\n"
        f"\t\t\t\t<xr:ObjectId>{uid(f'contained:{индекс}')}</xr:ObjectId>\n"
        f"\t\t\t</xr:ContainedObject>"
        for индекс, class_id in enumerate(CONTAINED_CLASS_IDS)
    )
    modules = "".join(f"\t\t\t<CommonModule>{PREFIX}{имя}</CommonModule>\n" for имя, _ in COMMON_MODULES)
    return f"""<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject {NS_SHORT} version="{VER}">
\t<Configuration uuid="{uid('configuration')}">
\t\t<InternalInfo>
{contained}
\t\t</InternalInfo>
\t\t<Properties>
\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>
\t\t\t<Name>{EXT_NAME}</Name>
{synonym(EXT_SYNONYM, 3)}
\t\t\t<Comment />
\t\t\t<ConfigurationExtensionPurpose>AddOn</ConfigurationExtensionPurpose>
\t\t\t<KeepMappingToExtendedConfigurationObjectsByIDs>true</KeepMappingToExtendedConfigurationObjectsByIDs>
\t\t\t<NamePrefix>{PREFIX}</NamePrefix>
\t\t\t<ConfigurationExtensionCompatibilityMode>{EXT_COMPAT}</ConfigurationExtensionCompatibilityMode>
\t\t\t<DefaultRunMode>ManagedApplication</DefaultRunMode>
\t\t\t<UsePurposes>
\t\t\t\t<v8:Value xsi:type="app:ApplicationUsePurpose">PlatformApplication</v8:Value>
\t\t\t</UsePurposes>
\t\t\t<ScriptVariant>Russian</ScriptVariant>
\t\t\t<Vendor />
\t\t\t<Version>{EXT_VERSION}</Version>
\t\t\t<DefaultLanguage>Language.Русский</DefaultLanguage>
\t\t\t<BriefInformation />
\t\t\t<DetailedInformation />
\t\t\t<Copyright />
\t\t\t<VendorInformationAddress />
\t\t\t<ConfigurationInformationAddress />
\t\t</Properties>
\t\t<ChildObjects>
\t\t\t<Language>Русский</Language>
{modules}\t\t\t<HTTPService>{SERVICE_NAME}</HTTPService>
\t\t</ChildObjects>
\t</Configuration>
</MetaDataObject>"""


# ExtendedConfigurationObject у языка НЕ указывается намеренно: это идентификатор
# языка в конкретной расширяемой конфигурации, и зашитый uuid делает поставку
# непереносимой — в чужой базе "Проверка возможности применения" ругается на
# несовпадение контролируемого свойства ОбъектРасширяемойКонфигурации. Без него
# язык сопоставляется по имени, и поставка встаёт в любую русскоязычную базу.
def language_xml():
    return f"""<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject {NS_FULL} version="{VER}">
\t<Language uuid="{uid('language')}">
\t\t<InternalInfo/>
\t\t<Properties>
\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>
\t\t\t<Name>Русский</Name>
\t\t\t<Comment/>
\t\t\t<LanguageCode>ru</LanguageCode>
\t\t</Properties>
\t</Language>
</MetaDataObject>"""


def http_service_xml():
    templates = []
    for name, template, method, handler in ENDPOINTS:
        # Синоним шаблона — первый сегмент адреса: у validate-query имя объекта не может
        # содержать дефис, а синоним показывает настоящий путь.
        подпись = template.strip("/").split("/")[0]
        templates.append(f"""\t\t\t<URLTemplate uuid="{uid(f'template:{name}')}">
\t\t\t\t<Properties>
\t\t\t\t\t<Name>{name}</Name>
{synonym(подпись, 5)}
\t\t\t\t\t<Comment/>
\t\t\t\t\t<Template>{template}</Template>
\t\t\t\t</Properties>
\t\t\t\t<ChildObjects>
\t\t\t\t\t<Method uuid="{uid(f'method:{name}')}">
\t\t\t\t\t\t<Properties>
\t\t\t\t\t\t\t<Name>{method.lower()}</Name>
{synonym(method, 7)}
\t\t\t\t\t\t\t<Comment/>
\t\t\t\t\t\t\t<HTTPMethod>{method}</HTTPMethod>
\t\t\t\t\t\t\t<Handler>{handler}</Handler>
\t\t\t\t\t\t</Properties>
\t\t\t\t\t</Method>
\t\t\t\t</ChildObjects>
\t\t\t</URLTemplate>""")
    return f"""<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject {NS_FULL} version="{VER}">
\t<HTTPService uuid="{uid('service')}">
\t\t<Properties>
\t\t\t<Name>{SERVICE_NAME}</Name>
{synonym("Сервис MCP", 3)}
\t\t\t<Comment/>
\t\t\t<RootURL>{ROOT_URL}</RootURL>
\t\t\t<ReuseSessions>DontUse</ReuseSessions>
\t\t\t<SessionMaxAge>20</SessionMaxAge>
\t\t</Properties>
\t\t<ChildObjects>
{chr(10).join(templates)}
\t\t</ChildObjects>
\t</HTTPService>
</MetaDataObject>"""


def common_module_xml(имя, синоним):
    return f"""<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject {NS_FULL} version="{VER}">
\t<CommonModule uuid="{uid(f'module:{имя}')}">
\t\t<Properties>
\t\t\t<Name>{PREFIX}{имя}</Name>
{synonym(синоним, 3)}
\t\t\t<Comment/>
\t\t\t<Global>false</Global>
\t\t\t<ClientManagedApplication>false</ClientManagedApplication>
\t\t\t<Server>true</Server>
\t\t\t<ExternalConnection>true</ExternalConnection>
\t\t\t<ClientOrdinaryApplication>true</ClientOrdinaryApplication>
\t\t\t<ServerCall>false</ServerCall>
\t\t\t<Privileged>false</Privileged>
\t\t\t<ReturnValuesReuse>DontUse</ReturnValuesReuse>
\t\t</Properties>
\t</CommonModule>
</MetaDataObject>"""


write(SRC / "Configuration.xml", configuration_xml())
write(SRC / "Languages/Русский.xml", language_xml())
write(SRC / f"HTTPServices/{SERVICE_NAME}.xml", http_service_xml())
for имя, синоним in COMMON_MODULES:
    write(SRC / f"CommonModules/{PREFIX}{имя}.xml", common_module_xml(имя, синоним))

# Модули живут в src и правятся там же: скрипт их не генерирует.
модули = [SRC / f"HTTPServices/{SERVICE_NAME}/Ext/Module.bsl"]
модули += [SRC / f"CommonModules/{PREFIX}{имя}/Ext/Module.bsl" for имя, _ in COMMON_MODULES]
for модуль in модули:
    if not модуль.exists():
        raise SystemExit(f"нет модуля {модуль.relative_to(ROOT)}")

print(f"UUID: переиспользовано {ПЕРЕИСПОЛЬЗОВАНО}, создано новых {СОЗДАНО}")
