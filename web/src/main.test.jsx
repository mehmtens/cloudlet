import React, {useState} from 'react';
import {render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {CloseAccountDialog, Login, NewFolderDialog} from './main.jsx';

const jsonResponse = (body, status = 200) => ({
  ok: status >= 200 && status < 300,
  status,
  json: vi.fn().mockResolvedValue(body),
});

function LoginHarness() {
  const [error, setError] = useState('');
  return <Login onSuccess={vi.fn()} error={error} setError={setError}/>;
}

describe('critical Cloudlet frontend behavior', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal('fetch', vi.fn());
  });

  it('switches between login and registration and preserves native validation', async () => {
    const user = userEvent.setup();
    render(<LoginHarness/>);

    expect(screen.getByRole('button', {name: 'Giriş yap'})).toBeVisible();
    const email = screen.getByLabelText('E-posta');
    const password = screen.getByLabelText('Parola');
    expect(email).toBeRequired();
    expect(password).toBeRequired();
    expect(password).toHaveAttribute('minlength', '12');

    await user.click(screen.getByRole('button', {name: /Hesabın yok mu/}));
    expect(screen.getByRole('button', {name: 'Hesap oluştur'})).toBeVisible();
    expect(screen.getByText('En az 12 karakter kullan.')).toBeVisible();

    await user.type(email, 'gecersiz');
    await user.type(password, 'kısa');
    expect(email).toBeInvalid();
    await user.click(screen.getByRole('button', {name: 'Hesap oluştur'}));
    expect(fetch).not.toHaveBeenCalled();
  });

  it('creates a folder and shows a rejected API-style error in the modal', async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn()
      .mockRejectedValueOnce(new Error('Bu klasör adı zaten kullanılıyor.'))
      .mockResolvedValueOnce(undefined);
    render(<NewFolderDialog onClose={vi.fn()} onCreate={onCreate}/>);

    expect(screen.getByRole('dialog', {name: 'Yeni klasör'})).toBeVisible();
    await user.type(screen.getByLabelText('Klasör adı'), 'Projeler');
    await user.click(screen.getByRole('button', {name: 'Klasör oluştur'}));
    expect(await screen.findByText('Bu klasör adı zaten kullanılıyor.')).toBeVisible();

    await user.clear(screen.getByLabelText('Klasör adı'));
    await user.type(screen.getByLabelText('Klasör adı'), 'Belgeler');
    await user.click(screen.getByRole('button', {name: 'Klasör oluştur'}));
    await waitFor(() => expect(onCreate).toHaveBeenLastCalledWith('Belgeler'));
  });

  it('validates the account password and exact deletion confirmation before calling the API', async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    fetch.mockResolvedValue(jsonResponse(null, 204));
    render(<CloseAccountDialog onClose={vi.fn()} onDone={onDone}/>);

    const password = screen.getByLabelText('Mevcut parola');
    const confirmation = screen.getByLabelText('Onaylamak için HESABIMI SİL yaz');
    expect(password).toBeRequired();
    expect(confirmation).toBeRequired();

    await user.type(password, 'Playwright-test-123!');
    await user.type(confirmation, 'hesabımı sil');
    await user.click(screen.getByRole('button', {name: 'Hesabı kalıcı olarak kapat'}));
    expect(await screen.findByText('Onay metnini aynen yazmalısın.')).toBeVisible();
    expect(fetch).not.toHaveBeenCalled();

    await user.clear(confirmation);
    await user.type(confirmation, 'HESABIMI SİL');
    await user.click(screen.getByRole('button', {name: 'Hesabı kalıcı olarak kapat'}));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/auth/account', expect.objectContaining({method: 'DELETE'})));
    expect(onDone).toHaveBeenCalledOnce();
  });

  it('shows an API error message returned during registration', async () => {
    const user = userEvent.setup();
    fetch.mockResolvedValue(jsonResponse({message: 'Bu e-posta adresi zaten kayıtlı.'}, 409));
    render(<LoginHarness/>);

    await user.click(screen.getByRole('button', {name: /Hesabın yok mu/}));
    await user.type(screen.getByLabelText('E-posta'), 'kayit@example.com');
    await user.type(screen.getByLabelText('Parola'), 'Yeterince-uzun-123!');
    await user.click(screen.getByRole('button', {name: 'Hesap oluştur'}));

    expect(await screen.findByText('Bu e-posta adresi zaten kayıtlı.')).toBeVisible();
  });
});
