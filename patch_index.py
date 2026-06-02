import re

with open('webpanel/index.html', 'r') as f:
    content = f.read()

# Disable specific inputs in the risk panel
inputs_to_disable = [
    'risk-per-symbol-quote',
    'risk-max-cycle-open-notional',
    'risk-max-cycle-loss',
    'risk-symbol-cooldown',
    'risk-platform-minor-cooldown',
    'risk-platform-cooldown',
    'risk-platform-critical-cooldown',
    'risk-max-daily-loss',
    'risk-max-daily-orders',
    'risk-max-daily-open-notional'
]

for inp in inputs_to_disable:
    pattern = r'(<input\s+id="' + inp + r'"[^>]*?)(>)'
    # ensure it doesn't already have disabled
    def repl(m):
        if 'disabled' not in m.group(1):
            return m.group(1) + ' disabled' + m.group(2)
        return m.group(0)
    content = re.sub(pattern, repl, content)

# Remove or hide risk actions buttons
risk_actions_patterimport re

with open('webpanel/index.html',"t
with opisk    content = f.read()

# Disable specific inputs in the rtn
# Disable specific i*</inputs_to_disable = [
    'risk-per-syat    'risk-per-syss    'risk-max-cycle-open-notioe;"    'risk-max-cycle-loss',
    n "'risk-symbol-cooldown'"is    'risk-platform-minor-coold      'risk-platform-cooldown',
    ="    'risk-platform-critical-cool??    'risk-max-daily-loss',
    'rnt    'risk-max-daily-orders'' "'risk-max-daily-open-notion']