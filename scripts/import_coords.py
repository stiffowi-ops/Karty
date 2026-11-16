#!/usr/bin/env python3
"""Import VERIFIED Yandex Constructor Point coordinates into config/overrides.json.

Preview by default. Only --write modifies JSON; existing descriptions/ratings are kept.
"""
import argparse
import csv
import json
import re
import sys
import unicodedata
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def clean(value):
    s = unicodedata.normalize('NFKC', str(value or '')).casefold().replace('ё', 'е')
    s = re.sub(r'^(?:\s*\d{1,3}\s*[-._ ]*)+', '', s)
    s = re.sub(r'^\s*(?:(?:станция|ст\.)\s+)?(?:метро|м\.)\s*', '', s)
    s = re.sub(r'[^\w]+', ' ', s, flags=re.UNICODE)
    return ' '.join(s.split())


def value(row, *keys):
    mapped = {clean(k): v for k, v in row.items() if k is not None}
    for key in keys:
        v = mapped.get(clean(key))
        if v is not None and str(v).strip():
            return v
    return None


def parse_float(raw):
    return float(str(raw).strip().replace(',', '.'))


def rows_from_csv(path):
    with path.open('r', encoding='utf-8-sig', newline='') as f:
        sample = f.read(4096)
        f.seek(0)
        try:
            delim = csv.Sniffer().sniff(sample, delimiters=',;\t').delimiter
        except csv.Error:
            delim = ';' if sample.count(';') > sample.count(',') else ','
        for i, row in enumerate(csv.DictReader(f, delimiter=delim), 2):
            title = value(row, 'name', 'title', 'название', 'подпись', 'имя', 'station')
            lat = value(row, 'lat', 'latitude', 'широта')
            lon = value(row, 'lon', 'lng', 'longitude', 'долгота')
            yield (str(i), title, lat, lon)


def rows_from_geojson(path):
    data = json.loads(path.read_text(encoding='utf-8-sig'))
    feats = data.get('features') if data.get('type') == 'FeatureCollection' else [data]
    if not isinstance(feats, list):
        raise ValueError('Ожидался GeoJSON FeatureCollection')
    for i, feature in enumerate(feats, 1):
        if not isinstance(feature, dict) or feature.get('type') != 'Feature':
            continue
        geo = feature.get('geometry') or {}
        if geo.get('type') != 'Point':
            continue  # polygons and lines are not stations
        coords = geo.get('coordinates') or []
        if len(coords) < 2:
            continue
        props = feature.get('properties') or {}
        title = value(props, 'name', 'title', 'подпись', 'название', 'hintContent', 'balloonContentHeader', 'iconCaption')
        yield (str(i), title, coords[1], coords[0])  # GeoJSON: longitude, latitude


def convert(source, seed_file, overrides_file):
    seed = json.loads(seed_file.read_text(encoding='utf-8'))
    overrides = json.loads(overrides_file.read_text(encoding='utf-8')) if overrides_file.exists() else {}
    by_name = defaultdict(list)
    for item in seed:
        by_name[clean(item['title'])].append(item['id'])
    candidates, issues = defaultdict(list), []
    loader = rows_from_geojson if source.suffix.lower() in ('.geojson', '.json') else rows_from_csv
    for rowno, title, lat_raw, lon_raw in loader(source):
        if not title:
            issues.append(f'Запись {rowno}: нет названия, пропущена')
            continue
        key = clean(title)
        matches = by_name.get(key, [])
        if len(matches) != 1:
            issues.append(f'Запись {rowno}: {title!r}, совпадений: {len(matches)}')
            continue
        try:
            lat, lon = parse_float(lat_raw), parse_float(lon_raw)
            if not (-90 <= lat <= 90 and -180 <= lon <= 180):
                raise ValueError('вне допустимого диапазона')
            if not all(map(lambda v: v == v and abs(v) != float('inf'), (lat, lon))):
                raise ValueError('NaN/inf')
        except (ValueError, TypeError) as e:
            issues.append(f'Запись {rowno}: {title!r}, некорректные координаты: {e}')
            continue
        candidates[matches[0]].append((lat, lon, title))
    added = []
    for key, variants in sorted(candidates.items()):
        if len(variants) != 1:
            issues.append(f'ID {key}: несколько точек, нужна ручная проверка')
            continue
        lat, lon, title = variants[0]
        current = overrides.get(key, {})
        if current.get('lat') is not None or current.get('lon') is not None:
            if current.get('lat') != lat or current.get('lon') != lon:
                issues.append(f'ID {key}: уже есть координаты; автоматически не перезаписываю')
            continue
        overrides.setdefault(key, {}).update({'lat': lat, 'lon': lon})
        added.append((key, title, lat, lon))
    return overrides, added, issues


def main(argv=None):
    ap = argparse.ArgumentParser(description='Сопоставление координат GeoJSON/CSV с 40 карточками бота')
    ap.add_argument('source', type=Path, help='Экспорт карты в .geojson, .json или .csv')
    ap.add_argument('--seed', type=Path, default=ROOT/'config/seed.json')
    ap.add_argument('--overrides', type=Path, default=ROOT/'config/overrides.json')
    ap.add_argument('--write', action='store_true', help='Сохранить однозначные новые совпадения')
    args = ap.parse_args(argv)
    try:
        result, added, issues = convert(args.source, args.seed, args.overrides)
    except (OSError, ValueError, json.JSONDecodeError) as e:
        ap.error(str(e))
    for key, title, lat, lon in added:
        print(f'+ {key} {title}: {lat:.6f}, {lon:.6f}')
    for issue in issues:
        print('! ' + issue)
    print(f'Новых однозначных координат: {len(added)}; требует проверки: {len(issues)}')
    if args.write:
        if added:
            args.overrides.parent.mkdir(parents=True, exist_ok=True)
            args.overrides.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
            print('Записано:', args.overrides)
        else:
            print('Нечего записывать, файл не изменён')
    else:
        print('Предпросмотр: файл НЕ изменён; для записи добавь --write')
    return 0


if __name__ == '__main__':
    sys.exit(main())
