"""Export the FDE course as it runs today on agent.hrtiger.cn into seed/fde-content.json (SRV-003).

Read-only: it reads the client repository's platform/shared/seed.json, platform/scripts/lab_guides.json and the
lab-workswarm-tools definition in platform/scripts/prepare_workswarm_demo.py, and writes one JSON file here.
It never touches a database or the original 智教云. Usage:

    python3 scripts/export_fde_content.py ../edu-agent-jiuwen seed/fde-content.json
"""
import importlib.util
import json
import subprocess
import sys
from pathlib import Path

COURSE = 'course-fde'
TOOLS_LAB = 'lab-workswarm-tools'
# The project page cards, as the client draws them today (ProjectCards.tsx); icons are lucide names.
CATEGORIES = [
    {'id': 'fde', 'title': 'FDE 项目', 'eyebrow': 'Forward Deployed Engineer', 'status': 'live',
     'summary': '面向企业 AI 交付：从场景判断、知识工程到智能体部署上线。',
     'flow': [{'label': '场景', 'icon': 'Compass'}, {'label': '知识', 'icon': 'Library'},
              {'label': '智能体', 'icon': 'Bot'}, {'label': '交付', 'icon': 'Rocket'}]},
    {'id': 'ai-comic', 'title': 'AI 漫剧项目', 'eyebrow': 'AI Comic Drama', 'status': 'coming_soon',
     'summary': '用 AI 完成剧本、分镜、角色与成片，做出一部完整的漫剧。',
     'flow': [{'label': '剧本', 'icon': 'ScrollText'}, {'label': '分镜', 'icon': 'LayoutGrid'},
              {'label': '角色', 'icon': 'Palette'}, {'label': '成片', 'icon': 'Clapperboard'}]},
]


def main(client, out):
    platform = Path(client) / 'platform'
    seed = json.loads((platform / 'shared/seed.json').read_text(encoding='utf-8'))
    guides = json.loads((platform / 'scripts/lab_guides.json').read_text(encoding='utf-8'))
    spec = importlib.util.spec_from_file_location('prepare', platform / 'scripts/prepare_workswarm_demo.py')
    prepare = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(prepare)  # module level only builds constants; main() is not called

    course = next(c for c in seed['courses'] if c['id'] == COURSE)
    labs = sorted((l for l in seed['labs'] if l['course_id'] == COURSE), key=lambda l: l['number'])
    missing = [l['id'] for l in labs if l['id'] not in guides]
    if missing:
        sys.exit(f'labs without a guide: {missing}')
    exported = []
    for lab in labs:
        guide = guides[lab['id']]
        exported.append({
            'source_id': lab['id'], 'chapter_source_id': lab['chapter_id'], 'title': lab['title'],
            'level': lab['level'], 'duration_minutes': lab['duration'],
            'description': guide['description'], 'objectives': lab['objectives'],
            'scenario': guide['scenario'], 'deliverables': guide['deliverables'],
            'boundary': guide['guide']['boundary'], 'guide_steps': guide['guide']['steps']})
    # The tools lab sits in the chapter of L20, after it (same placement prepare_workswarm_demo.py uses).
    tools = prepare.STRUCTURED
    chapter = next(l['chapter_id'] for l in labs if l['id'] == 'lab-fde-20')
    at = next(i for i, l in enumerate(exported) if l['source_id'] == 'lab-fde-20') + 1
    exported.insert(at, {
        'source_id': TOOLS_LAB, 'chapter_source_id': chapter, 'title': prepare.TASK['title'],
        'level': prepare.TASK['level'], 'duration_minutes': prepare.TASK['duration'],
        'description': tools['description'], 'objectives': prepare.TASK['objectives'],
        'scenario': tools['scenario'], 'deliverables': tools['deliverables'],
        'boundary': tools['guide']['boundary'], 'guide_steps': tools['guide']['steps']})
    revision = subprocess.run(['git', '-C', client, 'rev-parse', 'HEAD'], capture_output=True, text=True).stdout.strip()
    document = {
        'source': {'repository': 'edu-agent-jiuwen', 'commit': revision,
                   'files': ['platform/shared/seed.json', 'platform/scripts/lab_guides.json', 'platform/scripts/prepare_workswarm_demo.py']},
        'categories': CATEGORIES,
        'courses': [{
            'source_id': COURSE, 'category_id': 'fde', 'title': course['title'], 'description': course['description'],
            'chapters': [{'source_id': c['id'], 'title': c['title'], 'content': c.get('content', '')} for c in course['chapters']],
            'labs': exported}],
        'rubrics': [{'lab_source_id': TOOLS_LAB, 'title': prepare.RUBRIC['title'],
                     'items': prepare.RUBRIC['items'], 'references': prepare.RUBRIC['references']}],
    }
    Path(out).write_text(json.dumps(document, ensure_ascii=False, indent=1) + '\n', encoding='utf-8')
    print(f"exported {len(document['courses'][0]['chapters'])} chapters, {len(exported)} labs, "
          f"{len(document['rubrics'])} rubric from {revision[:7]}")


if __name__ == '__main__':
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
