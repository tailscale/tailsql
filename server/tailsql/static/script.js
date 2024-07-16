import { Params, Area, Cycle, Loop } from './sprite.js';

(() => {
    const query    = document.getElementById('query');
    const qButton  = document.getElementById('send-query');
    const dlButton = document.getElementById("dl-button");
    const qform    = document.getElementById('qform');
    const output   = document.getElementById('output');
    const base     = document.location.origin + document.location.pathname;
    const sources  = document.getElementById('sources');
    const body     = document.getElementById('tsql');

    const nuts = /a?corn|\bnut\b|seed|squirrel|tailsql/i;
    const velo = 5, delay = 60, runChance = 0.03;
    let hasRun = false;

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

    // Disable the download button when there are no query results.
    window.addEventListener("load", disableIfNoOutput(dlButton));
    // Initially focus the query input.
    window.addEventListener("load", (evt) => { query.focus(); });
    // Refresh when the input source changes.
    sources.addEventListener('change', (evt) => { qform.submit() });
})()
