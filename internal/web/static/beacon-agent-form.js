for (const form of document.querySelectorAll('.bc-agent-form')) {
  const owner = form.querySelector('select[name="owner"]');
  const preset = form.querySelector('select[name="preset"]');
  const presetPreview = form.querySelector('#agent-preset-preview');
  const selected = form.querySelector('[name="machine_access"][value="selected"]');
  const checkboxes = [...form.querySelectorAll('[name="machine"]')];
  const machinePreview = form.querySelector('.bc-machine-options output');
  const number = new Intl.NumberFormat(document.documentElement.lang);

  function filterForOwner() {
    if (!owner) return;
    const scopes = new Set(owner.selectedOptions[0].dataset.scopes.split(', '));
    for (const option of preset.options) {
      option.disabled = option.value !== 'custom' && option.dataset.scopes.split(', ').some(scope => !scopes.has(scope));
    }
    if (preset.selectedOptions[0]?.disabled) preset.value = preset.querySelector('option:not(:disabled)').value;
    for (const scope of form.querySelectorAll('[name="scopes"]')) {
      scope.disabled = !scopes.has(scope.value);
    }
  }

  function preview() {
    filterForOwner();
    if (presetPreview) presetPreview.value = preset.selectedOptions[0]?.dataset.scopes || '';
    if (!machinePreview) return;
    const count = checkboxes.filter(checkbox => checkbox.checked).length;
    const incomplete = selected.checked && count === 0;
    machinePreview.value = incomplete ? machinePreview.dataset.empty : machinePreview.dataset.count.replace('{count}', number.format(count));
    checkboxes[0].setCustomValidity(incomplete ? machinePreview.dataset.empty : '');
  }

  form.addEventListener('change', preview);
  window.addEventListener('pageshow', preview);
  preview();
}
