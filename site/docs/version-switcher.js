// Version switcher for docs pages.
// Reads versions.json and adds a version selector to the sidebar.
(function () {
  var sidebar = document.querySelector('.doc-sidebar');
  if (!sidebar) return;

  // Determine base path — /docs/ or /docs/vX.Y/
  var path = window.location.pathname;
  var versionMatch = path.match(/\/docs\/(v\d+\.\d+)\//);
  var currentVersion = versionMatch ? versionMatch[1] : 'latest';
  var jsonPath = versionMatch ? '/docs/versions.json' : '/docs/versions.json';

  fetch(jsonPath)
    .then(function (r) { return r.json(); })
    .then(function (versions) {
      if (!versions.length) return;

      var section = document.createElement('div');
      section.className = 'sidebar-section';
      section.innerHTML =
        '<div class="sidebar-heading">Version</div>' +
        '<select id="version-select" style="' +
        'width:100%;padding:6px 8px;background:var(--ds-surface);' +
        'color:var(--ds-text);border:1px solid var(--ds-border);' +
        'border-radius:var(--ds-radius);font-size:var(--ds-text-sm);' +
        'cursor:pointer;margin-top:4px;' +
        '"></select>';

      var select = section.querySelector('select');

      // Add "latest" option
      var latest = document.createElement('option');
      latest.value = '/docs/';
      latest.textContent = 'latest (' + versions[0].release + ')';
      if (currentVersion === 'latest') latest.selected = true;
      select.appendChild(latest);

      // Add versioned options
      versions.forEach(function (v) {
        var opt = document.createElement('option');
        opt.value = v.path;
        opt.textContent = v.version + ' (' + v.release + ')';
        if (currentVersion === v.version) opt.selected = true;
        select.appendChild(opt);
      });

      select.addEventListener('change', function () {
        // Navigate to same page in different version
        var page = path.split('/').pop() || 'index.html';
        window.location.href = select.value + page;
      });

      sidebar.appendChild(section);
    })
    .catch(function () { /* versions.json not available — skip */ });
})();
