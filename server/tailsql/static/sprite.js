// Module sprite defines a basic sprite animation library.

// Animation parameters.
export class Params {
    // Construct parameters for a sprite sheet of the given dimensions.
    constructor(width, height, nrow, ncol) {
        this.width = width;
        this.height = height;
        this.nrow = nrow;
        this.ncol = ncol;
        this.fh = height / nrow;
        this.fw = width / ncol;
    }
}

// An Area defines the region where an animation will play.
// The figure is an element containing the sprite sheet as a background image.
// Animation is constrained to the bounding box of the parent element of the figure.
export class Area {
    #img; #params; #hcap; #vcap; #locx; #locy; #wrap;

    // Initialize an area for the given figure and parameters.
    // The figure is the element containing the sprite image.
    // The params are a Params instance describing its parameters.
    //
    // Options:
    //   startx is the initial percentage (0..100) of the parent width,
    //   starty is the initial percentage (0..100) of the parent height,
    //   wrap is whether to wrap around at the end of a cycle.
    constructor({figure, params, startx=0, starty=0, wrap=true} = {}) {
        if (figure === undefined || params === undefined) {
            throw new Error("missing required parameters");
        }
        this.#img = figure;
        this.#params = params;
        const box = figure.parentElement.getBoundingClientRect();
        this.#hcap = 100 * params.fw/box.width;
        this.#vcap = 100 * params.fh/box.height;
        this.#locx = startx;
        this.#locy = starty;
        this.#wrap = wrap;
        this.moveFigure();
    }

    static pin100(v, cap) {
        if (v < -cap) {
            return {wrap: true, next: 100-cap};
        } else if (v >= 100) {
            return {wrap: true, next: -cap};
        }
        return {wrap: false, next: v};
    }

    // Show or hide the figure.
    setVisible(ok) {
        this.#img.style.visibility = ok ? 'visible' : 'hidden';
    }

    // Move the specified sprite (1-indexed) into the viewport.
    setFrame(row, col) {
        let rowOffset = (row - 1) * this.#params.fh;
        let colOffset = (col - 1) * this.#params.fw;

        this.#img.style.backgroundPosition = `-${colOffset}px -${rowOffset}px`;
    }

    // Move the figure to the current location.
    moveFigure() {
        this.#img.style.left = `${this.#locx}%`;
        this.#img.style.top = `${this.#locy}%`;
    }

    // Reset the location to the specified percentage offsets (0..100).
    resetLocation(px, py) {
        this.#locx = px;
        this.#locy = py;
        this.moveFigure();
    }

    // Update the location by dx, dy and report whether either direction
    // reached a boundary. If a boundary was reached and the area allows
    // wrapping, the update wraps; otherwise the update is discarded in that
    // dimension.
    updateLocation(dx, dy) {
        let nx = Area.pin100(this.#locx + dx, this.#hcap);
        if (!nx.wrap || this.#wrap) {
            this.#locx = nx.next;
        }
        let ny = Area.pin100(this.#locy + dy, this.#vcap);
        if (!ny.wrap || this.#wrap) {
            this.#locy = ny.next;
        }
        return nx.wrap || ny.wrap;
    }
}

// A Cycle represents the current state of an animation loop.
// Call update to advance to the next frame of the cycle.
export class Cycle {
    #curf; #loop;

    constructor(loop) {
        this.#loop = loop;
        this.#curf = 0;
    }
    update(area) {
        let col = this.#loop.frames[this.#curf];
        area.setFrame(this.#loop.row, col);
        area.moveFigure()
        let ok = area.updateLocation(this.#loop.vx/this.#loop.nframes, this.#loop.vy/this.#loop.nframes);

        this.#curf += 1;
        if (this.#curf >= this.#loop.nframes) {
            this.#curf = 0;
        }
        return ok;
    }
}

// A Loop represents a sequence of animation frames.
export class Loop {
    #vx; #vy; #row; #frames;

    constructor(vx, vy, row, frames) {
        this.#vx = vx;
        this.#vy = vy;
        this.#row = row;
        this.#frames = frames;
    }
    get vx() { return this.#vx; }
    get vy() { return this.#vy; }
    get row() { return this.#row; }
    get frames() { return this.#frames; }
    get nframes() { return this.#frames.length; }
}
