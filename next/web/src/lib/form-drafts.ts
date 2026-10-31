// Form controls keep their starting value until they leave the page.
type FormControl = { type: string; value: string; isConnected: boolean; checked?: boolean };
const formValue = (control: FormControl): string | boolean =>
  control.type === "checkbox" || control.type === "radio" ? control.checked ?? false : control.value;
export class FormDrafts {
  private initial = new WeakMap<FormControl, string | boolean>();
  private changed = new Set<FormControl>();

  focus(control: FormControl): void {
    if (!this.initial.has(control)) this.initial.set(control, formValue(control));
  }
  input(control: FormControl): void {
    if (formValue(control) === this.initial.get(control)) this.changed.delete(control);
    else this.changed.add(control);
  }
  get pending(): boolean {
    for (const control of this.changed) {
      if (control.isConnected && formValue(control) !== this.initial.get(control)) return true;
      this.changed.delete(control);
    }
    return false;
  }
}
