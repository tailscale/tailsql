import { Params, Area, Cycle, Loop } from './sprite.js';

(() => {
    const query        = document.getElementById('query');
    const qButton      = document.getElementById('send-query');
    const dlButton     = document.getElementById("dl-button");
    const saveButton   = document.getElementById('save-query');
    const qform        = document.getElementById('qform');
    const output       = document.getElementById('output');
    const base         = document.location.origin + document.location.pathname;
    const sources      = document.getElementById('sources');
    const body         = document.getElementById('tsql');
    const historyList  = document.getElementById('history-list');
    const clearHistory = document.getElementById('clear-history');
    const savedList    = document.getElementById('saved-query-list');

    const nuts = /a?corn|\bnut\b|seed|squirrel|tailsql/i;
    const velo = 5, delay = 60, runChance = 0.03;
    let hasRun = false;

    const LOCALSTORAGE_KEY_HISTORY = 'tailsql-history';
    const LOCALSTORAGE_KEY_SAVED   = 'tailsql-saved';
    const MAX_HISTORY_ENTRIES = 20;

    const param = new Params(256, 256, 8, 8);
    const aRunRight = new Loop(velo, 0, 5, [5,1,2,3]);
    const aRunLeft = new Loop(-velo, 0, 6, [5,1,2,3]);

    function hasQuery() {
        return query.value.trim() != "";
    }

    function shouldSquirrel() {
        return !hasRun &&
            (query.value.trim().match(nuts) ||
             new Date().toTimeString().slice(0, 5) == "16:20"
            ) &&
            Math.random() < runChance;
    }

    function maybeRunSquirrel() {
        if (!shouldSquirrel()) {
            return;
        }
        // Squirrel art from:
        //   http://saralara93.blogspot.com/2014/03/concept-art-part-3-squirrel.html

        const nut = document.getElementById("nut");
        if (nut === null) { return; } // UI not configured

        const isOdd = query.value.length%2 == 1;
        const area = new Area({
            figure: nut,
            params: param,
            startx: isOdd ? 100 : 0,
            wrap:   false,
        });
        const cycle = new Cycle(isOdd ? aRunLeft : aRunRight);
        hasRun = true;
        area.setVisible(true);
        let timer = setInterval(() => {
            if (cycle.update(area)) {
                clearInterval(timer);
                timer = null;
                area.setVisible(false);
            }
        }, delay);
    }

    query.addEventListener("keydown", (evt) => {
        if (evt.shiftKey && evt.key == "Enter") {
            evt.preventDefault();
            if (hasQuery()) {
                qButton.click();
            }
        }
        maybeRunSquirrel()
    })

    body.addEventListener("keydown", (evt) => {
        if (evt.altKey) {
            var c = evt.code.match(/^Digit(\d)$/);
            if (c) {
                var v = parseInt(c[1]);
                if (v > 0 && v <= sources.options.length) {
                    evt.preventDefault();
                    sources.options[v-1].selected = true;
                }
            }
        }
    });

    function performDownload(name, url) {
        var link = document.createElement('a');
        link.setAttribute('href', url);
        link.setAttribute('download', name);
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
    }

    function disableIfNoOutput(elt) {
        return (evt) => {
            elt.disabled = !output;
        }
    }

    dlButton.addEventListener("click", (evt) => {
        var fd = new FormData(qform);
        var sp = new URLSearchParams(fd);
        var href = base + "csv?" + sp.toString();

        performDownload('query.csv', href);
    });

    function getHistory() {
        try { return JSON.parse(localStorage.getItem(LOCALSTORAGE_KEY_HISTORY)) || []; }
        catch { return []; }
    }

    function setHistory(h) {
        localStorage.setItem(LOCALSTORAGE_KEY_HISTORY, JSON.stringify(h));
    }

    function getSavedQueries() {
        try { return JSON.parse(localStorage.getItem(LOCALSTORAGE_KEY_SAVED)) || []; }
        catch { return []; }
    }

    function setSavedQueries(s) {
        localStorage.setItem(LOCALSTORAGE_KEY_SAVED, JSON.stringify(s));
    }

    function renderHistory() {
        historyList.innerHTML = '';
        const h = getHistory();
        for (const entry of h) {
            const li = document.createElement('li');
            const span = document.createElement('span');
            span.className = 'query-text';
            span.textContent = entry.query;
            span.title = entry.query;
            li.appendChild(span);
            li.addEventListener('click', () => {
                query.value = entry.query;
                if (sources && entry.source) {
                    for (const opt of sources.options) {
                        if (opt.value === entry.source) {
                            opt.selected = true;
                            break;
                        }
                    }
                }
                query.focus();
            });
            historyList.appendChild(li);
        }
    }

    function renderSavedQueries() {
        savedList.innerHTML = '';
        const s = getSavedQueries();
        for (let i = 0; i < s.length; i++) {
            const entry = s[i];
            const li = document.createElement('li');
            const name = document.createElement('span');
            name.className = 'query-name';
            name.textContent = entry.name + ':';
            const span = document.createElement('span');
            span.className = 'query-text';
            span.textContent = entry.query;
            span.title = entry.query;
            const del = document.createElement('button');
            del.className = 'delete-btn';
            del.textContent = '\u00d7';
            del.title = 'Delete saved query';
            del.addEventListener('click', (evt) => {
                evt.stopPropagation();
                const cur = getSavedQueries();
                cur.splice(i, 1);
                setSavedQueries(cur);
                renderSavedQueries();
            });
            li.appendChild(name);
            li.appendChild(span);
            li.appendChild(del);
            li.addEventListener('click', () => {
                query.value = entry.query;
                query.focus();
            });
            savedList.appendChild(li);
        }
    }

    function recordHistory() {
        const errorDiv = document.getElementById('error');
        const q = query.value.trim();
        if (!q || errorDiv || !output) return;

        const h = getHistory().filter(e => e.query !== q);
        h.unshift({ query: q, source: sources.value, ts: Date.now() });
        if (h.length > MAX_HISTORY_ENTRIES) h.length = MAX_HISTORY_ENTRIES;
        setHistory(h);
    }

    saveButton.addEventListener('click', (evt) => {
        evt.preventDefault();
        const q = query.value.trim();
        if (!q) return;
        const name = prompt('Name for this query:');
        if (!name) return;
        const s = getSavedQueries();
        s.push({ query: q, name: name.trim() });
        setSavedQueries(s);
        renderSavedQueries();
    });

    clearHistory.addEventListener('click', (evt) => {
        evt.stopPropagation();
        localStorage.removeItem(LOCALSTORAGE_KEY_HISTORY);
        renderHistory();
    });

    // Disable the download button when there are no query results.
    window.addEventListener("load", disableIfNoOutput(dlButton));
    // Initially focus the query input.
    window.addEventListener("load", (evt) => { query.focus(); });
    // Refresh when the input source changes.
    sources.addEventListener('change', (evt) => { qform.submit() });

    // Persist open/closed state of query panels across page reloads.
    document.querySelectorAll('#query-panels details').forEach((d) => {
        if (localStorage.getItem(d.id) === 'open') d.open = true;
        d.addEventListener('toggle', () => {
            if (d.open) localStorage.setItem(d.id, 'open');
            else localStorage.removeItem(d.id);
        });
    });

    recordHistory();
    renderHistory();
    renderSavedQueries();
})()
