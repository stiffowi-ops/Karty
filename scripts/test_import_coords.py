import json
import tempfile
import unittest
from pathlib import Path
from import_coords import convert, clean


class ImportCoordsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.dir = Path(self.temp.name)
        self.seed = self.dir/'seed.json'
        self.override = self.dir/'overrides.json'
        self.seed.write_text(json.dumps([
            {'id': '001', 'title': 'Метро Остров Мечты'},
            {'id': '017', 'title': 'Метро Троицк'},
        ], ensure_ascii=False), encoding='utf-8')
        self.override.write_text('{"001":{"description":"Текст","rating":"8/10"}}', encoding='utf-8')

    def test_normalization(self):
        self.assertEqual(clean('017 МЕТРО Троицк'), 'троицк')
        self.assertEqual(clean('Метро Остров Мечты'), 'остров мечты')

    def test_geojson_and_keep_description(self):
        p = self.dir/'map.geojson'
        p.write_text(json.dumps({'type': 'FeatureCollection', 'features': [
            {'type': 'Feature', 'properties': {'name': '001 Метро Остров Мечты'}, 'geometry': {'type': 'Point', 'coordinates': [37.65, 55.69]}},
            {'type': 'Feature', 'properties': {'name': 'Метро Троицк'}, 'geometry': {'type': 'Point', 'coordinates': [37.3, 55.5]}},
            {'type': 'Feature', 'properties': {'name': 'Метро Неизвестная'}, 'geometry': {'type': 'Point', 'coordinates': [37.2, 55.2]}},
            {'type': 'Feature', 'properties': {'name': 'Полигон'}, 'geometry': {'type': 'Polygon', 'coordinates': []}},
        ]}, ensure_ascii=False), encoding='utf-8')
        result, added, issues = convert(p, self.seed, self.override)
        self.assertEqual(len(added), 2)
        self.assertEqual(len(issues), 1)
        self.assertEqual(result['001']['description'], 'Текст')
        self.assertEqual(result['001']['rating'], '8/10')
        self.assertEqual(result['001']['lat'], 55.69)
        self.assertEqual(result['001']['lon'], 37.65)

    def test_csv_duplicate_and_existent(self):
        p = self.dir/'map.csv'
        p.write_text('Подпись;Широта;Долгота\nМетро Троицк;55,5;37,3\nМетро Троицк;55,6;37,4\nМетро Остров Мечты;55,7;37,7\n', encoding='utf-8-sig')
        self.override.write_text('{"001":{"lat":55.1,"lon":37.1,"description":"Не менять"}}', encoding='utf-8')
        result, added, issues = convert(p, self.seed, self.override)
        self.assertEqual(added, [])
        self.assertEqual(len(issues), 2)
        self.assertEqual(result['001']['lat'], 55.1)
        self.assertNotIn('017', result)


if __name__ == '__main__':
    unittest.main()
