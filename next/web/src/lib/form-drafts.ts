// Form controls keep their starting value until they leave the page.
type FormControl = { value: string; isConnected: boolean };
export class FormDrafts {
  private initial = new WeakMap<FormControl, string>();
  private changed = new Set<FormControl>();

  focus(control: FormControl): void {
    if (!this.initial.has(control)) this.initial.set(control, control.value);
  }
  input(control: FormControl): void {
    if (control.value === this.initial.get(control)) this.changed.delete(control);
    else this.changed.add(control);
  }
  get pending(): boolean {
    for (const control of this.changed) {
      if (control.isConnected && control.value !== this.initial.get(control)) return true;
      this.changed.delete(control);
    }
    return false;
  }
}
